package store

import (
	"context"

	"github.com/google/uuid"
)

// ArchiveCandidate is one terminal run selected for log archival. The central
// log copy is moved by the API process, which owns LogRoot.
type ArchiveCandidate struct {
	RunID   uuid.UUID
	Attempt int
}

// ClaimRunsForArchive selects and marks runs whose logs are outside the job's
// retention policy, returning the claimed runs to the caller for file movement.
//
// Retention is a union: keep a run when it is either within
// job.log_retention_days or among the newest job.log_retention_runs completed
// runs of the job. A run is archived only when both conditions say it is
// outside. The
// advisory lock makes concurrent servers harmless; SKIP LOCKED avoids blocking.
func (s *Store) ClaimRunsForArchive(ctx context.Context, limit int) ([]ArchiveCandidate, bool, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, false, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended('sched:log-archive', 0))`).
		Scan(&locked); err != nil {
		return nil, false, wrapErr(err)
	}
	if !locked {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, wrapErr(err)
		}
		return nil, false, nil
	}

	rows, err := tx.Query(ctx, `
		SELECT r.id, r.attempt
		FROM run r
		JOIN job j ON j.id = r.job_id
		WHERE r.archived_at IS NULL
		  AND r.state IN ('SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT')
		  AND COALESCE(r.finished_at, r.created_at) <
		      clock_timestamp() - make_interval(days => j.log_retention_days)
		  AND (
		    SELECT count(*)
		    FROM (
		      SELECT 1 FROM run newer
		      WHERE newer.job_id = r.job_id
		        AND newer.state IN ('SUCCEEDED', 'FAILED', 'CANCELED', 'TIMED_OUT')
		        AND (newer.created_at, newer.id) > (r.created_at, r.id)
		      LIMIT j.log_retention_runs
		    ) ranked
		  ) >= j.log_retention_runs
		ORDER BY COALESCE(r.finished_at, r.created_at) ASC, r.id
		LIMIT $1
		FOR UPDATE OF r SKIP LOCKED`, limit)
	if err != nil {
		return nil, false, wrapErr(err)
	}
	var candidates []ArchiveCandidate
	for rows.Next() {
		var c ArchiveCandidate
		if err := rows.Scan(&c.RunID, &c.Attempt); err != nil {
			rows.Close()
			return nil, false, wrapErr(err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, wrapErr(err)
	}
	if len(candidates) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, false, wrapErr(err)
		}
		return nil, true, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE run SET archived_at = clock_timestamp(), version = version + 1
		WHERE id = ANY($1::uuid[])`, candidateIDs(candidates)); err != nil {
		return nil, false, wrapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, false, wrapErr(err)
	}
	return candidates, true, nil
}

func candidateIDs(candidates []ArchiveCandidate) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(candidates))
	for _, c := range candidates {
		ids = append(ids, c.RunID)
	}
	return ids
}
