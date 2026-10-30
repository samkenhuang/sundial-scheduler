package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"scheduler-platform/internal/domain"
)

// ClaimedRun is the worker-facing lease. The command snapshot is the immutable
// spec captured when the run was created; workers never read job_version.
type ClaimedRun struct {
	RunID      uuid.UUID
	LeaseToken uuid.UUID
	TimeHour   string
	Attempt    int
	Spec       domain.JobSpec

	// Log provenance. The Worker uses these fields to render the first log
	// line, so an operator can see where the build ran and what caused it.
	JobName            string
	LaunchMode         string
	TriggeredByRunID   *uuid.UUID
	TriggeredByJobName *string
}

// ClaimRuns leases up to limit QUEUED runs whose required labels are all in
// workerLabels. It is one transaction with SKIP LOCKED so multiple Workers can
// claim concurrently without handing the same run to two of them.
func (s *Store) ClaimRuns(ctx context.Context, workerID uuid.UUID, workerLabels []string, limit int) ([]ClaimedRun, error) {
	if limit <= 0 {
		return []ClaimedRun{}, nil
	}
	labels, err := json.Marshal(domain.NormalizeLabels(workerLabels))
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx, `
		WITH candidate AS (
			SELECT id FROM run
			WHERE state='QUEUED' AND available_at <= clock_timestamp()
			  AND required_labels <@ $2::jsonb
			ORDER BY priority DESC, available_at, created_at, id
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		),
		claimed AS (
			UPDATE run r
			SET state='RUNNING', lease_owner=$1, lease_token=gen_random_uuid(),
			    started_at=clock_timestamp(), version=r.version+1
			FROM candidate c
			WHERE r.id=c.id
			RETURNING r.id, r.lease_token, r.time_hour, r.attempt, r.command_snapshot,
			          r.job_id, r.launch_mode, r.triggered_by_run_id
		)
		SELECT c.id, c.lease_token, c.time_hour, c.attempt, c.command_snapshot,
		       j.name, c.launch_mode, c.triggered_by_run_id, upstream_job.name
		FROM claimed c
		JOIN job j ON j.id=c.job_id
		LEFT JOIN run upstream_run ON upstream_run.id=c.triggered_by_run_id
		LEFT JOIN job upstream_job ON upstream_job.id=upstream_run.job_id`,
		workerID, labels, limit)
	if err != nil {
		return nil, wrapErr(err)
	}
	out := []ClaimedRun{}
	for rows.Next() {
		var claimed ClaimedRun
		var snapshot []byte
		var triggeredBy uuid.NullUUID
		var upstreamJobName sql.NullString
		if err := rows.Scan(&claimed.RunID, &claimed.LeaseToken, &claimed.TimeHour, &claimed.Attempt, &snapshot,
			&claimed.JobName, &claimed.LaunchMode, &triggeredBy, &upstreamJobName); err != nil {
			rows.Close()
			return nil, wrapErr(err)
		}
		if err := json.Unmarshal(snapshot, &claimed.Spec); err != nil {
			rows.Close()
			return nil, wrapErr(err)
		}
		if triggeredBy.Valid {
			v := triggeredBy.UUID
			claimed.TriggeredByRunID = &v
		}
		if upstreamJobName.Valid {
			v := upstreamJobName.String
			claimed.TriggeredByJobName = &v
		}
		claimed.Spec.Normalize()
		out = append(out, claimed)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, wrapErr(err)
	}
	for _, claimed := range out {
		if _, err := tx.Exec(ctx, `
			INSERT INTO run_attempt (run_id, attempt, worker_id, state, started_at)
			VALUES ($1,$2,$3,'RUNNING',clock_timestamp())
			ON CONFLICT (run_id, attempt) DO UPDATE SET
				worker_id=EXCLUDED.worker_id, state='RUNNING', started_at=clock_timestamp(),
				finished_at=NULL, exit_code=NULL, error=NULL`,
			claimed.RunID, claimed.Attempt, workerID); err != nil {
			return nil, wrapErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapErr(err)
	}
	return out, nil
}

type CompleteRunInput struct {
	RunID      uuid.UUID
	WorkerID   uuid.UUID
	LeaseToken uuid.UUID
	ExitCode   *int
	TimedOut   bool
	Error      string
}

type CompleteRunResult struct {
	State   domain.RunState
	Attempt int
	Ignored bool
}

// CompleteRun is terminal by construction: FAILED/TIMED_OUT/SUCCEEDED are
// final, there is no retry or infra requeue. A duplicate completion for an
// already-terminal run is an idempotent no-op; a stale token while RUNNING is
// a lease loss.
func (s *Store) CompleteRun(ctx context.Context, in CompleteRunInput) (CompleteRunResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CompleteRunResult{}, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	var current domain.Run
	if err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM run WHERE id=$1 FOR UPDATE`, in.RunID), &current); err != nil {
		return CompleteRunResult{}, wrapErr(err)
	}
	if current.State.Terminal() {
		return CompleteRunResult{State: current.State, Attempt: current.Attempt, Ignored: true}, nil
	}
	if current.State != domain.RunRunning || current.LeaseOwner == nil || current.LeaseToken == nil ||
		*current.LeaseOwner != in.WorkerID || *current.LeaseToken != in.LeaseToken {
		return CompleteRunResult{}, ErrLeaseLost
	}

	target := domain.RunSucceeded
	switch {
	case current.CancelRequestedAt != nil:
		// A Worker reports a killed process as a signal/non-zero exit. The
		// durable cancel request is authoritative: the run is CANCELED, not
		// FAILED, even when the kill signal surfaces as a completion error.
		target = domain.RunCanceled
	case in.TimedOut:
		target = domain.RunTimedOut
	case in.ExitCode == nil || *in.ExitCode != 0:
		target = domain.RunFailed
	}
	if !domain.CanTransition(current.State, target) {
		return CompleteRunResult{}, ErrLeaseLost
	}
	errText := in.Error
	if target == domain.RunCanceled {
		errText = "canceled"
	} else if in.TimedOut && errText == "" {
		errText = "run timed out"
	}
	var updated domain.Run
	if err := scanRun(tx.QueryRow(ctx, `
		UPDATE run
		SET state=$2, lease_owner=NULL, lease_token=NULL,
		    finished_at=clock_timestamp(), version=version+1, error=NULLIF($3,'')
		WHERE id=$1 AND state='RUNNING' AND lease_token=$4
		RETURNING `+runColumns,
		in.RunID, string(target), errText, in.LeaseToken), &updated); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CompleteRunResult{}, ErrLeaseLost
		}
		return CompleteRunResult{}, wrapErr(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE run_attempt
		SET state=$3, exit_code=$4, error=NULLIF($5,''), finished_at=clock_timestamp()
		WHERE run_id=$1 AND attempt=$2`,
		in.RunID, current.Attempt, string(target), in.ExitCode, errText); err != nil {
		return CompleteRunResult{}, wrapErr(err)
	}
	if err := advanceDependencies(ctx, tx, &updated); err != nil {
		return CompleteRunResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CompleteRunResult{}, wrapErr(err)
	}
	return CompleteRunResult{State: target, Attempt: updated.Attempt}, nil
}

// CancelRun cancels a queued run immediately and signals a running run's
// Worker. A terminal run is returned unchanged for idempotent retries.
func (s *Store) CancelRun(ctx context.Context, id uuid.UUID) (domain.Run, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Run{}, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	var run domain.Run
	if err := scanRun(tx.QueryRow(ctx, `SELECT `+runColumns+` FROM run WHERE id=$1 FOR UPDATE`, id), &run); err != nil {
		return domain.Run{}, wrapErr(err)
	}
	if run.State.Terminal() {
		return run, nil
	}
	if run.State == domain.RunPending || run.State == domain.RunQueued {
		if err := scanRun(tx.QueryRow(ctx, `
			UPDATE run SET state='CANCELED', finished_at=clock_timestamp(),
				version=version+1, error='canceled'
			WHERE id=$1 AND state IN ('PENDING','QUEUED')
			RETURNING `+runColumns, id), &run); err != nil {
			return domain.Run{}, wrapErr(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Run{}, wrapErr(err)
		}
		return run, nil
	}
	if run.State != domain.RunRunning {
		return domain.Run{}, ErrConflict
	}
	if run.LeaseOwner == nil {
		return domain.Run{}, ErrLeaseLost
	}
	payload, _ := json.Marshal(map[string]any{"runId": id.String()})
	if _, err := tx.Exec(ctx, `
		INSERT INTO worker_command (worker_id, run_id, attempt, kind, payload, expires_at)
		VALUES ($1,$2,$3,'cancel',$4,clock_timestamp() + make_interval(secs => 60))`,
		*run.LeaseOwner, id, run.Attempt, payload); err != nil {
		return domain.Run{}, wrapErr(err)
	}
	if err := scanRun(tx.QueryRow(ctx, `
		UPDATE run SET cancel_requested_at=COALESCE(cancel_requested_at, clock_timestamp()),
			version=version+1
		WHERE id=$1 AND state='RUNNING'
		RETURNING `+runColumns, id), &run); err != nil {
		return domain.Run{}, wrapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Run{}, wrapErr(err)
	}
	return run, nil
}
