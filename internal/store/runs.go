package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"scheduler-platform/internal/domain"
)

type CreateRunInput struct {
	JobID          uuid.UUID
	JobVersionID   uuid.UUID
	LaunchMode     string
	Priority       int
	Spec           domain.JobSpec
	Inputs         any
	TimeHour       string
	CycleKey       *string
	IdempotencyKey *string
	DedupKey       *string
	ConcurrencyKey *string
	ScheduledAt    *time.Time
}

type CreateRunResult struct {
	Run     domain.Run
	Created bool
}

func (s *Store) CreateRun(ctx context.Context, in CreateRunInput) (CreateRunResult, error) {
	if in.TimeHour == "" {
		in.TimeHour = time.Now().UTC().Format(domain.TimeHourLayout)
	}
	if err := domain.ValidateTimeHour(in.TimeHour); err != nil {
		return CreateRunResult{}, err
	}
	specJSON, err := json.Marshal(in.Spec)
	if err != nil {
		return CreateRunResult{}, err
	}
	labelsJSON, err := json.Marshal(in.Spec.Labels)
	if err != nil {
		return CreateRunResult{}, err
	}
	var inputsJSON []byte
	if in.Inputs != nil {
		inputsJSON, err = json.Marshal(in.Inputs)
		if err != nil {
			return CreateRunResult{}, err
		}
	}
	var cycle, idem, dedup, conc any
	if in.CycleKey != nil {
		cycle = *in.CycleKey
	}
	if in.IdempotencyKey != nil {
		idem = *in.IdempotencyKey
	}
	if in.DedupKey != nil {
		dedup = *in.DedupKey
	}
	if in.ConcurrencyKey != nil {
		conc = *in.ConcurrencyKey
	}

	var run domain.Run
	err = scanRun(s.pool.QueryRow(ctx, `
		INSERT INTO run (
			job_id, job_version_id, launch_mode, state, priority,
			required_labels, time_hour, cycle_key, attempt, idempotency_key, dedup_key,
			concurrency_key, inputs, command_snapshot, scheduled_at, enqueued_at,
			available_at
		) VALUES (
			$1,$2,$3,'QUEUED',$4,$5,$6,$7,1,$8,$9,$10,$11,$12,$13,clock_timestamp(),
			clock_timestamp()
		)
		RETURNING `+runColumns,
		in.JobID, in.JobVersionID, in.LaunchMode, in.Priority,
		labelsJSON, in.TimeHour, cycle, idem, dedup, conc,
		inputsJSON, specJSON, in.ScheduledAt,
	), &run)
	if err == nil {
		return CreateRunResult{Run: run, Created: true}, nil
	}
	if !isUniqueViolation(err) {
		return CreateRunResult{}, wrapErr(err)
	}
	// Idempotency/dedup conflict: return the already-created run.
	if in.IdempotencyKey != nil {
		existing, lookupErr := s.findRunByIdempotency(ctx, in.JobID, *in.IdempotencyKey)
		if lookupErr == nil {
			return CreateRunResult{Run: existing, Created: false}, nil
		}
	}
	if in.DedupKey != nil {
		existing, lookupErr := s.findRunByDedup(ctx, *in.DedupKey)
		if lookupErr == nil {
			return CreateRunResult{Run: existing, Created: false}, nil
		}
	}
	return CreateRunResult{}, ErrConflict
}

func (s *Store) findRunByIdempotency(ctx context.Context, jobID uuid.UUID, key string) (domain.Run, error) {
	var run domain.Run
	err := scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM run WHERE job_id=$1 AND idempotency_key=$2`, jobID, key), &run)
	return run, err
}

func (s *Store) findRunByDedup(ctx context.Context, key string) (domain.Run, error) {
	var run domain.Run
	err := scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM run WHERE dedup_key=$1`, key), &run)
	return run, err
}

func (s *Store) GetRun(ctx context.Context, id uuid.UUID) (domain.Run, error) {
	var run domain.Run
	err := scanRun(s.pool.QueryRow(ctx, `SELECT `+runColumns+` FROM run WHERE id=$1`, id), &run)
	return run, err
}

// LatestRunWorker returns the Worker that most recently executed a run. It is
// used by the log recovery path; run.lease_owner is intentionally cleared when
// a run completes, so run_attempt is the durable ownership record.
func (s *Store) LatestRunWorker(ctx context.Context, runID uuid.UUID, attempt int) (uuid.UUID, bool, error) {
	var worker uuid.UUID
	err := s.pool.QueryRow(ctx, `
		SELECT worker_id FROM run_attempt
		WHERE run_id=$1 AND attempt=$2 AND worker_id IS NOT NULL`, runID, attempt).Scan(&worker)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, nil
	}
	if err != nil {
		return uuid.Nil, false, wrapErr(err)
	}
	return worker, true, nil
}

// RunCursor is the stable (created_at,id) position used for pagination. UUID
// ordering breaks ties when multiple runs share a timestamp.
type RunCursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

func EncodeRunCursor(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func DecodeRunCursor(encoded string) (RunCursor, error) {
	body, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return RunCursor{}, err
	}
	parts := strings.SplitN(string(body), "|", 2)
	if len(parts) != 2 {
		return RunCursor{}, errors.New("invalid cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return RunCursor{}, err
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return RunCursor{}, err
	}
	return RunCursor{CreatedAt: created, ID: id}, nil
}

type RunFilter struct {
	JobID  *uuid.UUID
	State  *domain.RunState
	Limit  int
	Cursor *RunCursor
}

type RunPage struct {
	Runs       []domain.Run
	NextCursor string
}

func (s *Store) ListRuns(ctx context.Context, f RunFilter) (RunPage, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	if f.Limit > 100 {
		f.Limit = 100
	}
	args := []any{}
	where := []string{"archived_at IS NULL"}
	if f.JobID != nil {
		args = append(args, *f.JobID)
		where = append(where, "job_id=$"+itoa(len(args)))
	}
	if f.State != nil {
		args = append(args, string(*f.State))
		where = append(where, "state=$"+itoa(len(args)))
	}
	if f.Cursor != nil {
		args = append(args, f.Cursor.CreatedAt, f.Cursor.ID)
		where = append(where, "(created_at, id) < ($"+itoa(len(args)-1)+", $"+itoa(len(args))+")")
	}
	args = append(args, f.Limit+1)
	rows, err := s.pool.Query(ctx, `SELECT `+runColumns+` FROM run
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY created_at DESC, id DESC LIMIT $`+itoa(len(args)), args...)
	if err != nil {
		return RunPage{}, wrapErr(err)
	}
	defer rows.Close()
	out := RunPage{Runs: []domain.Run{}}
	for rows.Next() {
		var run domain.Run
		if err := scanRun(rows, &run); err != nil {
			return RunPage{}, wrapErr(err)
		}
		out.Runs = append(out.Runs, run)
	}
	if err := rows.Err(); err != nil {
		return RunPage{}, wrapErr(err)
	}
	if len(out.Runs) > f.Limit {
		out.Runs = out.Runs[:f.Limit]
		last := out.Runs[len(out.Runs)-1]
		out.NextCursor = EncodeRunCursor(last.CreatedAt, last.ID)
	}
	return out, nil
}

func scanRun(row pgx.Row, r *domain.Run) error {
	var state, launchMode string
	var cycleKey, idem, dedup, conc, errText, timeHour sql.NullString
	var required, inputs, command []byte
	var scheduled, enqueued, started, cancel, deps, archived sql.NullTime
	var leaseOwner, leaseToken uuid.NullUUID
	var finishedAt sql.NullTime
	err := row.Scan(
		&r.ID, &r.JobID, &r.JobVersionID, &launchMode, &state, &r.Priority,
		&required, &timeHour, &cycleKey, &r.Attempt,
		&idem, &dedup, &conc, &inputs, &command,
		&scheduled, &enqueued, &r.AvailableAt, &leaseOwner, &leaseToken,
		&started, &finishedAt, &cancel, &errText, &deps, &archived, &r.Version, &r.CreatedAt,
	)
	if err != nil {
		return err
	}
	r.LaunchMode = launchMode
	r.State = domain.RunState(state)
	r.RequiredLabels = required
	r.Inputs = inputs
	r.CommandSnapshot = command
	r.TimeHour = timeHour.String
	if cycleKey.Valid {
		r.CycleKey = &cycleKey.String
	}
	if idem.Valid {
		r.IdempotencyKey = &idem.String
	}
	if dedup.Valid {
		r.DedupKey = &dedup.String
	}
	if conc.Valid {
		r.ConcurrencyKey = &conc.String
	}
	if scheduled.Valid {
		r.ScheduledAt = &scheduled.Time
	}
	if enqueued.Valid {
		r.EnqueuedAt = &enqueued.Time
	}
	if leaseOwner.Valid {
		v := leaseOwner.UUID
		r.LeaseOwner = &v
	}
	if leaseToken.Valid {
		v := leaseToken.UUID
		r.LeaseToken = &v
	}
	if started.Valid {
		r.StartedAt = &started.Time
	}
	if finishedAt.Valid {
		r.FinishedAt = &finishedAt.Time
	}
	if cancel.Valid {
		r.CancelRequestedAt = &cancel.Time
	}
	if errText.Valid {
		r.Error = &errText.String
	}
	if deps.Valid {
		r.DependenciesProcessedAt = &deps.Time
	}
	if archived.Valid {
		r.ArchivedAt = &archived.Time
	}
	return nil
}

const runColumns = `id, job_id, job_version_id, launch_mode, state, priority,
	required_labels, time_hour, cycle_key, attempt, idempotency_key, dedup_key,
	concurrency_key, inputs, command_snapshot, scheduled_at, enqueued_at, available_at,
	lease_owner, lease_token, started_at, finished_at,
	cancel_requested_at, error, dependencies_processed_at, archived_at, version, created_at`
