package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// WorkerRun is a historical build that executed an attempt on one executor.
// run.lease_owner is cleared on completion, so history comes from run_attempt.
type WorkerRun struct {
	RunID      uuid.UUID
	JobID      uuid.UUID
	JobName    string
	State      string
	LaunchMode string
	TimeHour   string
	Attempt    int
	StartedAt  *time.Time
	FinishedAt *time.Time
	Error      *string
}

func (s *Store) ListWorkerRuns(ctx context.Context, workerID uuid.UUID, limit int) ([]WorkerRun, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.job_id, j.name, r.state, r.launch_mode, r.time_hour,
		       a.attempt, COALESCE(r.started_at, a.started_at), r.finished_at, r.error
		FROM run_attempt a
		JOIN run r ON r.id = a.run_id
		JOIN job j ON j.id = r.job_id
		WHERE a.worker_id = $1
		ORDER BY COALESCE(r.finished_at, r.started_at, a.started_at, r.created_at) DESC, r.id DESC
		LIMIT $2`, workerID, limit)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := []WorkerRun{}
	for rows.Next() {
		var run WorkerRun
		if err := rows.Scan(&run.RunID, &run.JobID, &run.JobName, &run.State, &run.LaunchMode,
			&run.TimeHour, &run.Attempt, &run.StartedAt, &run.FinishedAt, &run.Error); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
