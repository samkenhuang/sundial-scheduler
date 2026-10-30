package store

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
)

const workerColumns = `id, name, description, labels, concurrency_limit, status,
	hostname, version, log_used_bytes, log_quota_bytes, registered_at,
	last_heartbeat_at, draining`

type workerScanner interface {
	Scan(dest ...any) error
}

func scanWorker(row workerScanner, w *domain.Worker) error {
	return row.Scan(&w.ID, &w.Name, &w.Description, &w.Labels, &w.ConcurrencyLimit, &w.Status,
		&w.Hostname, &w.Version, &w.LogUsedBytes, &w.LogQuotaBytes, &w.RegisteredAt,
		&w.LastHeartbeatAt, &w.Draining)
}

func defaultWorkerName(hostname string, id uuid.UUID) string {
	host := strings.TrimSpace(hostname)
	if host == "" {
		host = "executor"
	}
	return host + "-" + id.String()[:8]
}

type RegisterWorkerInput struct {
	ID               uuid.UUID
	Labels           []string
	ConcurrencyLimit int
	Hostname         string
	Version          string
	LogQuotaBytes    int64
}

func (s *Store) RegisterWorker(ctx context.Context, in RegisterWorkerInput) (domain.Worker, error) {
	labels, _ := json.Marshal(domain.NormalizeLabels(in.Labels))
	if in.ConcurrencyLimit <= 0 {
		in.ConcurrencyLimit = 16
	}
	var w domain.Worker
	err := s.pool.QueryRow(ctx, `
		INSERT INTO worker (id, name, description, labels, concurrency_limit, status,
			hostname, version, log_quota_bytes, registered_at, last_heartbeat_at, draining)
		VALUES ($1,$2,'',$3,$4,'ONLINE',$5,$6,$7,clock_timestamp(),clock_timestamp(),false)
		ON CONFLICT (id) DO UPDATE SET
			labels=EXCLUDED.labels, concurrency_limit=EXCLUDED.concurrency_limit,
			status=CASE WHEN worker.draining THEN 'DRAINING' ELSE 'ONLINE' END,
			hostname=EXCLUDED.hostname, version=EXCLUDED.version,
			log_quota_bytes=EXCLUDED.log_quota_bytes, last_heartbeat_at=clock_timestamp()
		RETURNING `+workerColumns,
		in.ID, defaultWorkerName(in.Hostname, in.ID), labels, in.ConcurrencyLimit,
		in.Hostname, in.Version, in.LogQuotaBytes,
	).Scan(&w.ID, &w.Name, &w.Description, &w.Labels, &w.ConcurrencyLimit, &w.Status,
		&w.Hostname, &w.Version, &w.LogUsedBytes, &w.LogQuotaBytes, &w.RegisteredAt,
		&w.LastHeartbeatAt, &w.Draining)
	if err != nil {
		return w, wrapErr(err)
	}
	return w, nil
}

// WorkerInput is the admin-managed identity of an executor. Runtime values
// (hostname/version/heartbeat) are supplied by the Worker registration path.
type WorkerInput struct {
	Name             string
	Description      string
	Labels           []string
	ConcurrencyLimit int
}

func (s *Store) CreateWorker(ctx context.Context, in WorkerInput) (domain.Worker, error) {
	labels, _ := json.Marshal(domain.NormalizeLabels(in.Labels))
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = defaultWorkerName("", uuid.New())
	}
	var w domain.Worker
	err := scanWorker(s.pool.QueryRow(ctx, `
		INSERT INTO worker (id, name, description, labels, concurrency_limit, status,
			hostname, version, log_quota_bytes, registered_at, last_heartbeat_at, draining)
		VALUES (gen_random_uuid(),$1,$2,$3,$4,'OFFLINE','','',0,clock_timestamp(),
			clock_timestamp() - interval '100 years',false)
		RETURNING `+workerColumns,
		name, in.Description, labels, in.ConcurrencyLimit,
	), &w)
	if err != nil {
		if isUniqueViolation(err) {
			return w, ErrConflict
		}
		return w, wrapErr(err)
	}
	return w, nil
}

func (s *Store) UpdateWorker(ctx context.Context, id uuid.UUID, in WorkerInput) (domain.Worker, error) {
	labels, _ := json.Marshal(domain.NormalizeLabels(in.Labels))
	var w domain.Worker
	err := scanWorker(s.pool.QueryRow(ctx, `
		UPDATE worker
		SET name=$2, description=$3, labels=$4, concurrency_limit=$5
		WHERE id=$1
		RETURNING `+workerColumns,
		id, strings.TrimSpace(in.Name), in.Description, labels, in.ConcurrencyLimit,
	), &w)
	if err != nil {
		if isUniqueViolation(err) {
			return w, ErrConflict
		}
		return w, wrapErr(err)
	}
	return w, nil
}

// DeleteWorker removes an idle executor. The NOT EXISTS guard makes deletion
// and claim mutually safe: an executor that owns a RUNNING build is busy and
// must finish/stop it first.
func (s *Store) DeleteWorker(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM worker w
		WHERE w.id=$1
		  AND NOT EXISTS (
			SELECT 1 FROM run r WHERE r.state='RUNNING' AND r.lease_owner=w.id
		  )`, id)
	if err != nil {
		return wrapErr(err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM worker WHERE id=$1)`, id).Scan(&exists); err != nil {
		return wrapErr(err)
	}
	if exists {
		return ErrWorkerBusy
	}
	return ErrNotFound
}

func (s *Store) GetWorker(ctx context.Context, id uuid.UUID) (domain.Worker, error) {
	var w domain.Worker
	err := scanWorker(s.pool.QueryRow(ctx, `SELECT `+workerColumns+` FROM worker WHERE id=$1`, id), &w)
	if err != nil {
		return w, wrapErr(err)
	}
	return w, nil
}

func (s *Store) ListWorkers(ctx context.Context) ([]domain.Worker, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+workerColumns+` FROM worker ORDER BY registered_at`)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := []domain.Worker{}
	for rows.Next() {
		var w domain.Worker
		if err := scanWorker(rows, &w); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) SetWorkerDraining(ctx context.Context, id uuid.UUID, draining bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE worker SET draining=$2, status=$3 WHERE id=$1`,
		id, draining, map[bool]string{true: "DRAINING", false: "ONLINE"}[draining])
	if err != nil {
		return wrapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RunningCount(ctx context.Context, workerID uuid.UUID) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM run WHERE state='RUNNING' AND lease_owner=$1`, workerID).Scan(&n)
	return n, wrapErr(err)
}

// TouchWorkerHeartbeat is the PostgreSQL liveness fallback, used only when the
// shared Redis presence store is unavailable. It is one row update per Worker,
// never per running run.
func (s *Store) TouchWorkerHeartbeat(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE worker SET last_heartbeat_at=clock_timestamp() WHERE id=$1`, id)
	return wrapErr(err)
}

// HeartbeatWorker revives an OFFLINE Worker. It writes the durable heartbeat
// timestamp only on the recovery transition, so healthy Redis-backed
// heartbeats do not generate a DB write every interval.
func (s *Store) HeartbeatWorker(ctx context.Context, id uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE worker SET
			status=CASE WHEN status='OFFLINE' THEN 'ONLINE' ELSE status END,
			last_heartbeat_at=CASE WHEN status='OFFLINE' THEN clock_timestamp() ELSE last_heartbeat_at END
		WHERE id=$1`, id)
	if err != nil {
		return wrapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkWorkerOffline is called by the leader when a Worker's presence expires.
// It never overrides an intentional DRAINING state.
func (s *Store) MarkWorkerOffline(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `UPDATE worker SET status='OFFLINE' WHERE id=$1 AND NOT draining`, id)
	return wrapErr(err)
}
