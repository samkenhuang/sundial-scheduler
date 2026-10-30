package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"scheduler-platform/internal/domain"
)

type SchedNode struct {
	ID          uuid.UUID
	Addr        string
	StartedAt   time.Time
	HeartbeatAt time.Time
	State       string
}

func (s *Store) RegisterSchedNode(ctx context.Context, id uuid.UUID, addr string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sched_node (id, addr, started_at, heartbeat_at, state)
		VALUES ($1,$2,clock_timestamp(),clock_timestamp(),'alive')
		ON CONFLICT (id) DO UPDATE SET addr=EXCLUDED.addr, heartbeat_at=clock_timestamp(), state='alive'`,
		id, addr)
	return wrapErr(err)
}

func (s *Store) HeartbeatSchedNode(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE sched_node SET heartbeat_at=clock_timestamp() WHERE id=$1`, id)
	return wrapErr(err)
}

func (s *Store) SetSchedNodeDraining(ctx context.Context, id uuid.UUID, draining bool) error {
	state := "alive"
	if draining {
		state = "draining"
	}
	_, err := s.pool.Exec(ctx, `UPDATE sched_node SET state=$2 WHERE id=$1`, id, state)
	return wrapErr(err)
}

// ListSchedNodes returns recently registered scheduler processes, including
// stale/draining ones, for the control-plane management page. It is bounded so
// a long-lived cluster does not page the full node history.
func (s *Store) ListSchedNodes(ctx context.Context, since time.Time, limit int) ([]SchedNode, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, addr, started_at, heartbeat_at, state FROM sched_node
		WHERE heartbeat_at >= $1
		ORDER BY heartbeat_at DESC LIMIT $2`, since, limit)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []SchedNode
	for rows.Next() {
		var n SchedNode
		if err := rows.Scan(&n.ID, &n.Addr, &n.StartedAt, &n.HeartbeatAt, &n.State); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) ListAliveSchedNodes(ctx context.Context, cutoff time.Time) ([]SchedNode, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, addr, started_at, heartbeat_at, state FROM sched_node
		WHERE state='alive' AND heartbeat_at > $1 ORDER BY started_at`, cutoff)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []SchedNode
	for rows.Next() {
		var n SchedNode
		if err := rows.Scan(&n.ID, &n.Addr, &n.StartedAt, &n.HeartbeatAt, &n.State); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

type DueJob struct {
	JobID    uuid.UUID
	NextFire time.Time
	Cron     string
	Timezone string
}

func (s *Store) ListDueJobs(ctx context.Context, limit int) ([]DueJob, error) {
	if limit <= 0 || limit > 1000 {
		limit = 1000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, next_fire_at, cron, timezone FROM job
		WHERE schedule_enabled AND cron IS NOT NULL AND next_fire_at <= clock_timestamp()
		ORDER BY next_fire_at LIMIT $1`, limit)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	var out []DueJob
	for rows.Next() {
		var j DueJob
		if err := rows.Scan(&j.JobID, &j.NextFire, &j.Cron, &j.Timezone); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FireDueJobCatchUp advances one job from dueAt through every missed cron
// occurrence, up to limit runs per call. It never skips an occurrence: if the
// limit is reached the job stays due and the next call continues from the same
// next_fire_at. The job row lock plus next_fire_at check keeps this idempotent
// across leader handover.
func (s *Store) FireDueJobCatchUp(ctx context.Context, jobID uuid.UUID, dueAt time.Time, limit int) (int, time.Time, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, dueAt, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	var currentVersion uuid.NullUUID
	var nextFire time.Time
	var cycleFormat, cron, timezone string
	err = tx.QueryRow(ctx, `
		SELECT current_version_id, next_fire_at, cycle_format, cron, timezone FROM job
		WHERE id=$1 FOR UPDATE`, jobID).Scan(&currentVersion, &nextFire, &cycleFormat, &cron, &timezone)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, dueAt, nil
	}
	if err != nil {
		return 0, dueAt, wrapErr(err)
	}
	if !nextFire.Equal(dueAt) || !currentVersion.Valid {
		return 0, dueAt, nil
	}
	var specJSON []byte
	if err := tx.QueryRow(ctx, `SELECT spec FROM job_version WHERE id=$1`, currentVersion.UUID).Scan(&specJSON); err != nil {
		return 0, dueAt, wrapErr(err)
	}
	var spec domain.JobSpec
	if err := json.Unmarshal(specJSON, &spec); err != nil {
		return 0, dueAt, fmt.Errorf("decode job spec: %w", err)
	}
	spec.Normalize()
	labels, _ := json.Marshal(spec.Labels)
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return 0, dueAt, wrapErr(err)
	}

	fired := 0
	lastDue := time.Time{}
	for fired < limit && !nextFire.After(now) {
		cycleKey, err := domain.CycleKey(cycleFormat, timezone, nextFire)
		if err != nil {
			return 0, dueAt, fmt.Errorf("cycle key for job %s: %w", jobID, err)
		}
		timeHour, err := domain.TimeHourAt(nextFire, timezone)
		if err != nil {
			return 0, dueAt, fmt.Errorf("time_hour for job %s: %w", jobID, err)
		}
		dedup := "cron:" + jobID.String() + ":" + nextFire.UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(ctx, `
			INSERT INTO run (
				job_id, job_version_id, launch_mode, state, priority,
				required_labels, time_hour, cycle_key, dedup_key, command_snapshot,
				scheduled_at, enqueued_at, available_at
			) VALUES (
				$1,$2,'cron','QUEUED',$3,$4,$5,$6,$7,$8,$9,clock_timestamp(),$9
			) ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`,
			jobID, currentVersion.UUID, spec.Priority, labels, timeHour, cycleKey,
			dedup, specJSON, nextFire); err != nil {
			return 0, dueAt, wrapErr(err)
		}
		lastDue = nextFire
		fired++
		next, err := domain.NextFire(cron, timezone, nextFire)
		if err != nil {
			return 0, dueAt, fmt.Errorf("next fire for job %s: %w", jobID, err)
		}
		nextFire = next
	}
	if fired > 0 {
		if _, err := tx.Exec(ctx, `UPDATE job SET next_fire_at=$2, last_fire_at=$3 WHERE id=$1`,
			jobID, nextFire, lastDue); err != nil {
			return 0, dueAt, wrapErr(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, dueAt, wrapErr(err)
	}
	return fired, nextFire, nil
}

// RunningOwner is a read-only snapshot of a Worker that owns RUNNING runs.
// Worker liveness is checked in Redis by the Scheduler; DBFresh is only the
// Redis-failure fallback.
type RunningOwner struct {
	WorkerID uuid.UUID
	DBFresh  bool // computed by PostgreSQL clock_timestamp(), not app time
}

// RunningOwners returns distinct Workers with RUNNING runs. This is one small
// global query per liveness poll; liveness itself remains Redis-first.
func (s *Store) RunningOwners(ctx context.Context, limit int, ttl time.Duration) ([]RunningOwner, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.lease_owner,
		       COALESCE(w.last_heartbeat_at >= clock_timestamp() - make_interval(secs => $2), false)
		FROM run r
		LEFT JOIN worker w ON w.id = r.lease_owner
		WHERE r.state='RUNNING' AND r.lease_owner IS NOT NULL
		GROUP BY r.lease_owner, w.last_heartbeat_at
		ORDER BY r.lease_owner
		LIMIT $1`, limit, ttl.Seconds())
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := []RunningOwner{}
	for rows.Next() {
		var owner RunningOwner
		if err := rows.Scan(&owner.WorkerID, &owner.DBFresh); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, owner)
	}
	return out, rows.Err()
}

// FailWorkerRuns marks all RUNNING runs owned by a dead Worker as FAILED.
// Failures are terminal; there is no infra requeue and no retry.
func (s *Store) FailWorkerRuns(ctx context.Context, workerID uuid.UUID, limit int) (int64, error) {
	if limit <= 0 {
		limit = 100
	}
	var failed int64
	err := s.pool.QueryRow(ctx, `
		WITH failed AS (
			UPDATE run SET state='FAILED', version=version+1,
				lease_owner=NULL, lease_token=NULL,
				finished_at=clock_timestamp(), error='worker offline',
				dependencies_processed_at=clock_timestamp()
			WHERE id IN (
				SELECT id FROM run
				WHERE state='RUNNING' AND lease_owner=$1
				ORDER BY started_at
				LIMIT $2
				FOR UPDATE SKIP LOCKED
			)
			RETURNING id, attempt
		), closed AS (
			UPDATE run_attempt a SET state='FAILED', error='worker offline',
				finished_at=clock_timestamp()
			FROM failed f
			WHERE a.run_id=f.id AND a.attempt=f.attempt AND a.finished_at IS NULL
			RETURNING 1
		)
		SELECT count(*) FROM failed`, workerID, limit).Scan(&failed)
	if err != nil {
		return 0, wrapErr(err)
	}
	return failed, nil
}
