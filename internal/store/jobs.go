package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
)

type CreateJobInput struct {
	Name        string
	ProjectID   *uuid.UUID
	CreatedBy   string
	CycleFormat string
}

type JobFilter struct {
	ProjectID *uuid.UUID
	Query     string
	Limit     int
}

func (s *Store) CreateJob(ctx context.Context, in CreateJobInput) (domain.Job, error) {
	var job domain.Job
	var projectID any
	if in.ProjectID != nil {
		projectID = *in.ProjectID
	}
	cycleFormat := in.CycleFormat
	if cycleFormat == "" {
		cycleFormat = "2006-01-02"
	}
	err := scanJob(s.pool.QueryRow(ctx, `
		INSERT INTO job (name, project_id, created_by, cycle_format)
		VALUES ($1, $2, $3, $4)
		RETURNING `+jobColumns,
		in.Name, projectID, in.CreatedBy, cycleFormat,
	), &job)
	if isUniqueViolation(err) {
		return job, ErrConflict
	}
	if err != nil {
		return job, wrapErr(err)
	}
	return job, nil
}

func (s *Store) GetJob(ctx context.Context, id uuid.UUID) (domain.Job, error) {
	var job domain.Job
	err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM job WHERE id=$1`, id), &job)
	if err != nil {
		return job, wrapErr(err)
	}
	return job, nil
}

func (s *Store) GetJobByName(ctx context.Context, name string) (domain.Job, error) {
	var job domain.Job
	err := scanJob(s.pool.QueryRow(ctx, `SELECT `+jobColumns+` FROM job WHERE name=$1`, name), &job)
	if err != nil {
		return job, wrapErr(err)
	}
	return job, nil
}

// JobNames resolves a set of job IDs to names in one query. List endpoints use
// this instead of issuing one GetJob per run.
func (s *Store) JobNames(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := make(map[uuid.UUID]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `SELECT id, name FROM job WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, wrapErr(err)
		}
		out[id] = name
	}
	return out, rows.Err()
}

func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]domain.Job, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	args := []any{}
	where := []string{"archived_at IS NULL"}
	if f.ProjectID != nil {
		args = append(args, *f.ProjectID)
		where = append(where, "project_id=$"+itoa(len(args)))
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		args = append(args, "%"+q+"%")
		where = append(where, "name ILIKE $"+itoa(len(args)))
	}
	args = append(args, f.Limit)
	rows, err := s.pool.Query(ctx, `SELECT `+jobColumns+` FROM job WHERE `+strings.Join(where, " AND ")+`
		ORDER BY name LIMIT $`+itoa(len(args)), args...)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := []domain.Job{}
	for rows.Next() {
		var job domain.Job
		if err := scanJob(rows, &job); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func (s *Store) PublishJobVersion(ctx context.Context, jobID uuid.UUID, spec domain.JobSpec, hash, actor string) (domain.JobVersion, error) {
	spec.Normalize()
	body, err := json.Marshal(spec)
	if err != nil {
		return domain.JobVersion{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.JobVersion{}, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM job WHERE id=$1 AND archived_at IS NULL)`, jobID).Scan(&exists); err != nil {
		return domain.JobVersion{}, wrapErr(err)
	}
	if !exists {
		return domain.JobVersion{}, ErrNotFound
	}

	var version int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM job_version WHERE job_id=$1`, jobID).Scan(&version); err != nil {
		return domain.JobVersion{}, wrapErr(err)
	}
	var out domain.JobVersion
	err = tx.QueryRow(ctx, `
		INSERT INTO job_version (job_id, version, spec, spec_hash, created_by)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, job_id, version, spec, spec_hash, created_by, created_at`,
		jobID, version, body, hash, actor,
	).Scan(&out.ID, &out.JobID, &out.Version, &out.Spec, &out.SpecHash, &out.CreatedBy, &out.CreatedAt)
	if err != nil {
		return domain.JobVersion{}, wrapErr(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE job SET current_version_id=$2 WHERE id=$1`, jobID, out.ID); err != nil {
		return domain.JobVersion{}, wrapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.JobVersion{}, wrapErr(err)
	}
	return out, nil
}

func (s *Store) GetJobVersion(ctx context.Context, jobID uuid.UUID, version int) (domain.JobVersion, error) {
	var out domain.JobVersion
	err := s.pool.QueryRow(ctx, `
		SELECT id, job_id, version, spec, spec_hash, created_by, created_at
		FROM job_version WHERE job_id=$1 AND version=$2`, jobID, version,
	).Scan(&out.ID, &out.JobID, &out.Version, &out.Spec, &out.SpecHash, &out.CreatedBy, &out.CreatedAt)
	if err != nil {
		return out, wrapErr(err)
	}
	return out, nil
}

func (s *Store) GetCurrentJobVersion(ctx context.Context, jobID uuid.UUID) (domain.JobVersion, error) {
	var out domain.JobVersion
	err := s.pool.QueryRow(ctx, `
		SELECT jv.id, jv.job_id, jv.version, jv.spec, jv.spec_hash, jv.created_by, jv.created_at
		FROM job j JOIN job_version jv ON jv.id = j.current_version_id
		WHERE j.id=$1`, jobID,
	).Scan(&out.ID, &out.JobID, &out.Version, &out.Spec, &out.SpecHash, &out.CreatedBy, &out.CreatedAt)
	if err != nil {
		return out, wrapErr(err)
	}
	return out, nil
}

type UpdateScheduleInput struct {
	Cron            *string
	Timezone        string
	ScheduleEnabled bool
	NextFireAt      *time.Time
}

func (s *Store) UpdateSchedule(ctx context.Context, jobID uuid.UUID, in UpdateScheduleInput) (domain.Job, error) {
	var job domain.Job
	err := scanJob(s.pool.QueryRow(ctx, `
		UPDATE job SET cron=$2, timezone=$3, schedule_enabled=$4, next_fire_at=$5
		WHERE id=$1 AND archived_at IS NULL
		RETURNING `+jobColumns,
		jobID, in.Cron, in.Timezone, in.ScheduleEnabled, in.NextFireAt,
	), &job)
	if err != nil {
		return job, wrapErr(err)
	}
	return job, nil
}

func (s *Store) UpdateLogRetention(ctx context.Context, jobID uuid.UUID, runs, days int) (domain.Job, error) {
	var job domain.Job
	err := scanJob(s.pool.QueryRow(ctx, `
		UPDATE job SET log_retention_runs=$2, log_retention_days=$3
		WHERE id=$1 AND archived_at IS NULL
		RETURNING `+jobColumns,
		jobID, runs, days,
	), &job)
	if err != nil {
		return job, wrapErr(err)
	}
	return job, nil
}

// RenameJob changes a job's display name. The name remains globally unique;
// an archived job keeps its reserved name and cannot be renamed.
func (s *Store) RenameJob(ctx context.Context, id uuid.UUID, name string) (domain.Job, error) {
	var job domain.Job
	err := scanJob(s.pool.QueryRow(ctx, `
		UPDATE job SET name=$2 WHERE id=$1 AND archived_at IS NULL
		RETURNING `+jobColumns, id, name), &job)
	if isUniqueViolation(err) {
		return job, ErrConflict
	}
	if err != nil {
		return job, wrapErr(err)
	}
	return job, nil
}

// ArchiveJob is a soft archive: the job row, its versions, runs and logs stay
// for audit. Cron is disabled, dependency edges are removed, and queued runs
// become CANCELED. A RUNNING run is allowed to finish naturally.
func (s *Store) ArchiveJob(ctx context.Context, id uuid.UUID) (domain.Job, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Job{}, 0, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	var job domain.Job
	err = scanJob(tx.QueryRow(ctx, `
		SELECT `+jobColumns+` FROM job WHERE id=$1 FOR UPDATE`, id), &job)
	if err != nil {
		return domain.Job{}, 0, wrapErr(err)
	}
	if job.ArchivedAt != nil {
		return job, 0, nil
	}
	if err := scanJob(tx.QueryRow(ctx, `
		UPDATE job SET archived_at=clock_timestamp(), schedule_enabled=false, next_fire_at=NULL
		WHERE id=$1
		RETURNING `+jobColumns, id), &job); err != nil {
		return domain.Job{}, 0, wrapErr(err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM job_dependency
		WHERE downstream_job_id=$1 OR upstream_job_id=$1`, id); err != nil {
		return domain.Job{}, 0, wrapErr(err)
	}
	tag, err := tx.Exec(ctx, `
		UPDATE run
		SET state='CANCELED', version=version+1,
		    finished_at=clock_timestamp(), error='job archived'
		WHERE job_id=$1 AND state IN ('PENDING','QUEUED')`, id)
	if err != nil {
		return domain.Job{}, 0, wrapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Job{}, 0, wrapErr(err)
	}
	return job, tag.RowsAffected(), nil
}

func (s *Store) Audit(ctx context.Context, actor, action, resourceType, resourceID string, before, after any) error {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_log (actor_type, actor_id, action, resource_type, resource_id, before, after)
		VALUES ('user', $1, $2, $3, $4, $5, $6)`,
		actor, action, resourceType, resourceID, b, a)
	return err
}

const jobColumns = `id, project_id, name, current_version_id, cron, timezone,
	schedule_enabled, next_fire_at, last_fire_at, created_by, created_at, archived_at,
	cycle_format, log_retention_runs, log_retention_days`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner, job *domain.Job) error {
	return row.Scan(
		&job.ID, &job.ProjectID, &job.Name, &job.CurrentVersionID,
		&job.Cron, &job.Timezone, &job.ScheduleEnabled,
		&job.NextFireAt, &job.LastFireAt, &job.CreatedBy, &job.CreatedAt, &job.ArchivedAt,
		&job.CycleFormat, &job.LogRetentionRuns, &job.LogRetentionDays,
	)
}
