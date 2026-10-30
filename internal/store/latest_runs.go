package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/google/uuid"
)

// JobLastRun is the latest run summary for a job, used by list UIs.
type JobLastRun struct {
	JobID      uuid.UUID
	RunID      uuid.UUID
	State      string
	LaunchMode string
	TimeHour   string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	Error      *string
}

// LatestRunsByJob returns one latest run per requested job. It is a single
// DISTINCT ON query over run(job_id, created_at), not one query per job.
func (s *Store) LatestRunsByJob(ctx context.Context, jobIDs []uuid.UUID) (map[uuid.UUID]JobLastRun, error) {
	out := map[uuid.UUID]JobLastRun{}
	if len(jobIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (job_id)
			job_id, id, state, launch_mode, time_hour, created_at,
			started_at, finished_at, error
		FROM run
		WHERE job_id = ANY($1::uuid[]) AND archived_at IS NULL
		ORDER BY job_id, created_at DESC, id DESC`, jobIDs)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var r JobLastRun
		var started, finished sql.NullTime
		var errText sql.NullString
		if err := rows.Scan(&r.JobID, &r.RunID, &r.State, &r.LaunchMode, &r.TimeHour,
			&r.CreatedAt, &started, &finished, &errText); err != nil {
			return nil, wrapErr(err)
		}
		if started.Valid {
			r.StartedAt = &started.Time
		}
		if finished.Valid {
			r.FinishedAt = &finished.Time
		}
		if errText.Valid {
			r.Error = &errText.String
		}
		out[r.JobID] = r
	}
	return out, wrapErr(rows.Err())
}
