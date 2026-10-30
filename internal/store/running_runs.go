package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// RunningRun is the minimal executor-facing snapshot of a RUNNING build. It is
// read from the run's Worker ownership (`lease_owner`), not from the scheduler:
// the scheduler triggers/queues work, while the Worker owns the live build.
type RunningRun struct {
	WorkerID   uuid.UUID
	RunID      uuid.UUID
	JobID      uuid.UUID
	JobName    string
	LaunchMode string
	TimeHour   string
	Attempt    int
	StartedAt  time.Time
}

// ListRunningRuns returns RUNNING builds in Worker/start order. The partial
// index on run(lease_owner) WHERE state='RUNNING' bounds this to live builds;
// the cap only protects against a pathological number of concurrent runs.
func (s *Store) ListRunningRuns(ctx context.Context, limit int) ([]RunningRun, error) {
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.lease_owner, r.id, r.job_id, j.name, r.launch_mode,
		       r.time_hour, r.attempt, COALESCE(r.started_at, r.created_at)
		FROM run r
		JOIN job j ON j.id = r.job_id
		WHERE r.state = 'RUNNING' AND r.lease_owner IS NOT NULL
		ORDER BY r.lease_owner, COALESCE(r.started_at, r.created_at), r.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()

	out := make([]RunningRun, 0, 16)
	for rows.Next() {
		var run RunningRun
		if err := scanRunningRun(rows, &run); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// ListRunningRunsForWorker is the detail-page counterpart to
// ListRunningRuns; it uses the same partial index with an owner equality.
func (s *Store) ListRunningRunsForWorker(ctx context.Context, workerID uuid.UUID, limit int) ([]RunningRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.lease_owner, r.id, r.job_id, j.name, r.launch_mode,
		       r.time_hour, r.attempt, COALESCE(r.started_at, r.created_at)
		FROM run r
		JOIN job j ON j.id = r.job_id
		WHERE r.state = 'RUNNING' AND r.lease_owner = $1
		ORDER BY COALESCE(r.started_at, r.created_at), r.id
		LIMIT $2`, workerID, limit)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := []RunningRun{}
	for rows.Next() {
		var run RunningRun
		if err := scanRunningRun(rows, &run); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func scanRunningRun(row workerScanner, run *RunningRun) error {
	return row.Scan(&run.WorkerID, &run.RunID, &run.JobID, &run.JobName, &run.LaunchMode,
		&run.TimeHour, &run.Attempt, &run.StartedAt)
}
