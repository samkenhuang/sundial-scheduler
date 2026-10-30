package store

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"scheduler-platform/internal/domain"
)

type DependencyEdge struct {
	UpstreamJobID uuid.UUID
	Condition     string // on_success | always
	Required      bool
}

type DependencyEdgeView struct {
	UpstreamJobID   uuid.UUID `json:"upstreamJobId,omitempty"`
	UpstreamName    string    `json:"upstreamName,omitempty"`
	DownstreamJobID uuid.UUID `json:"downstreamJobId,omitempty"`
	DownstreamName  string    `json:"downstreamName,omitempty"`
	Condition       string    `json:"condition"`
	Required        bool      `json:"required"`
}

// SetJobDependencies replaces a downstream job's upstream edges atomically.
// Graph checks run under a transaction-scoped advisory lock so two concurrent
// edits cannot each create one half of a cycle.
func (s *Store) SetJobDependencies(ctx context.Context, downstream uuid.UUID, edges []DependencyEdge) ([]DependencyEdge, error) {
	seen := map[uuid.UUID]struct{}{}
	for i := range edges {
		e := &edges[i]
		if e.UpstreamJobID == uuid.Nil || e.UpstreamJobID == downstream {
			return nil, ErrDependencyEdge
		}
		if _, ok := seen[e.UpstreamJobID]; ok {
			return nil, ErrDependencyEdge
		}
		seen[e.UpstreamJobID] = struct{}{}
		if e.Condition == "" {
			e.Condition = "on_success"
		}
		if e.Condition != "on_success" && e.Condition != "always" {
			return nil, ErrDependencyEdge
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(778899001)`); err != nil {
		return nil, wrapErr(err)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE id=$1 AND archived_at IS NULL)`, downstream).Scan(&exists); err != nil {
		return nil, wrapErr(err)
	}
	if !exists {
		return nil, ErrNotFound
	}
	if len(edges) > 0 {
		ids := make([]uuid.UUID, 0, len(edges))
		for _, e := range edges {
			ids = append(ids, e.UpstreamJobID)
		}
		var found int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM job WHERE id = ANY($1::uuid[]) AND archived_at IS NULL`, ids).Scan(&found); err != nil {
			return nil, wrapErr(err)
		}
		if found != len(ids) {
			return nil, ErrDependencyEdge
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM job_dependency WHERE downstream_job_id=$1`, downstream); err != nil {
		return nil, wrapErr(err)
	}
	for _, e := range edges {
		if _, err := tx.Exec(ctx, `
			INSERT INTO job_dependency (downstream_job_id, upstream_job_id, condition, required)
			VALUES ($1,$2,$3,$4)`,
			downstream, e.UpstreamJobID, e.Condition, e.Required); err != nil {
			if isUniqueViolation(err) {
				return nil, ErrDependencyEdge
			}
			return nil, wrapErr(err)
		}
	}
	// Fan-out: an upstream edge may not feed more than 500 downstream jobs.
	var over int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM (
			SELECT upstream_job_id FROM job_dependency GROUP BY upstream_job_id HAVING count(*) > 500
		) x`).Scan(&over); err != nil {
		return nil, wrapErr(err)
	}
	if over > 0 {
		return nil, ErrDependencyFanout
	}
	// Reachability from downstream through upstream->downstream edges. A hit on
	// downstream means the proposed replacement creates a cycle. Maximum depth
	// is bounded to keep the recursive CTE finite even for large graphs.
	var hasCycle bool
	var maxDepth int
	if err := tx.QueryRow(ctx, `
		WITH RECURSIVE reach(job_id, depth) AS (
			SELECT d.downstream_job_id, 1
			FROM job_dependency d WHERE d.upstream_job_id=$1
			UNION
			SELECT d.downstream_job_id, r.depth+1
			FROM job_dependency d JOIN reach r ON d.upstream_job_id = r.job_id
			WHERE r.depth < 100
		)
		SELECT EXISTS(SELECT 1 FROM reach WHERE job_id=$1),
		       COALESCE(MAX(depth), 0) FROM reach`, downstream).Scan(&hasCycle, &maxDepth); err != nil {
		return nil, wrapErr(err)
	}
	if hasCycle {
		return nil, ErrDependencyCycle
	}
	if maxDepth >= 100 {
		return nil, ErrDependencyDepth
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapErr(err)
	}
	return edges, nil
}

func (s *Store) ListJobDependencies(ctx context.Context, jobID uuid.UUID) (upstreams, downstreams []DependencyEdgeView, err error) {
	upstreams, downstreams = []DependencyEdgeView{}, []DependencyEdgeView{}
	rows, err := s.pool.Query(ctx, `
		SELECT d.upstream_job_id, j.name, d.condition, d.required
		FROM job_dependency d JOIN job j ON j.id=d.upstream_job_id
		WHERE d.downstream_job_id=$1 ORDER BY j.name`, jobID)
	if err != nil {
		return nil, nil, wrapErr(err)
	}
	for rows.Next() {
		var v DependencyEdgeView
		if err := rows.Scan(&v.UpstreamJobID, &v.UpstreamName, &v.Condition, &v.Required); err != nil {
			rows.Close()
			return nil, nil, wrapErr(err)
		}
		upstreams = append(upstreams, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, wrapErr(err)
	}
	rows, err = s.pool.Query(ctx, `
		SELECT d.downstream_job_id, j.name, d.condition, d.required
		FROM job_dependency d JOIN job j ON j.id=d.downstream_job_id
		WHERE d.upstream_job_id=$1 ORDER BY j.name`, jobID)
	if err != nil {
		return nil, nil, wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v DependencyEdgeView
		if err := rows.Scan(&v.DownstreamJobID, &v.DownstreamName, &v.Condition, &v.Required); err != nil {
			return nil, nil, wrapErr(err)
		}
		downstreams = append(downstreams, v)
	}
	return upstreams, downstreams, rows.Err()
}

// advanceDependencies triggers downstream runs from the successful upstream
// run history. A downstream job is ready when every required upstream job has
// at least one unarchived SUCCEEDED run with the same time_hour. Archived runs
// are no longer part of readiness. Each successful upstream run may trigger a
// new downstream run (backfills/reruns are allowed).
func advanceDependencies(ctx context.Context, tx pgx.Tx, run *domain.Run) error {
	if !run.State.Terminal() || run.DependenciesProcessedAt != nil {
		return nil
	}
	if run.State != domain.RunSucceeded || run.TimeHour == "" {
		return markDependenciesProcessed(ctx, tx, run.ID)
	}
	rows, err := tx.Query(ctx, `
		SELECT downstream_job_id
		FROM job_dependency
		WHERE upstream_job_id=$1 AND required`, run.JobID)
	if err != nil {
		return wrapErr(err)
	}
	var downstreams []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return wrapErr(err)
		}
		downstreams = append(downstreams, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return wrapErr(err)
	}
	sort.Slice(downstreams, func(i, j int) bool {
		return downstreams[i].String() < downstreams[j].String()
	})

	for _, downstream := range downstreams {
		// Concurrent upstream completions can each miss the other before commit.
		// Serialize readiness checks for the same (downstream, time_hour), so the
		// second transaction sees the first commit.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			downstream.String()+":"+run.TimeHour); err != nil {
			return wrapErr(err)
		}
		var required, missing int
		err := tx.QueryRow(ctx, `
			SELECT count(*), count(*) FILTER (WHERE NOT EXISTS (
				SELECT 1 FROM run r
				WHERE r.job_id=e.upstream_job_id
				  AND r.time_hour=$2 AND r.state='SUCCEEDED'
				  AND r.archived_at IS NULL
			))
			FROM job_dependency e
			WHERE e.downstream_job_id=$1 AND e.required`,
			downstream, run.TimeHour).Scan(&required, &missing)
		if err != nil {
			return wrapErr(err)
		}
		if required == 0 || missing > 0 {
			continue
		}

		var versionID uuid.NullUUID
		if err := tx.QueryRow(ctx, `SELECT current_version_id FROM job WHERE id=$1`, downstream).
			Scan(&versionID); err != nil {
			return wrapErr(err)
		}
		if !versionID.Valid {
			continue
		}
		var specJSON []byte
		if err := tx.QueryRow(ctx, `SELECT spec FROM job_version WHERE id=$1`, versionID.UUID).
			Scan(&specJSON); err != nil {
			return wrapErr(err)
		}
		var spec domain.JobSpec
		if err := json.Unmarshal(specJSON, &spec); err != nil {
			return err
		}
		spec.Normalize()
		labels, _ := json.Marshal(spec.Labels)
		dedup := "dep:" + downstream.String() + ":" + run.TimeHour + ":" + run.ID.String()
		if _, err := tx.Exec(ctx, `
			INSERT INTO run (
				job_id, job_version_id, launch_mode, state, priority,
				required_labels, time_hour, cycle_key, dedup_key, triggered_by_run_id,
				command_snapshot, enqueued_at, available_at
			) VALUES (
				$1,$2,'dependency','QUEUED',$3,$4,$5,NULL,$6,$7,$8,
				clock_timestamp(),clock_timestamp()
			) ON CONFLICT (dedup_key) WHERE dedup_key IS NOT NULL DO NOTHING`,
			downstream, versionID.UUID, spec.Priority, labels, run.TimeHour,
			dedup, run.ID, specJSON); err != nil {
			return wrapErr(err)
		}
	}
	return markDependenciesProcessed(ctx, tx, run.ID)
}

func markDependenciesProcessed(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE run SET dependencies_processed_at=clock_timestamp()
		WHERE id=$1 AND dependencies_processed_at IS NULL`, id)
	return wrapErr(err)
}
