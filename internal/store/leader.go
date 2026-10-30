package store

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaderAdvisoryKey is the PostgreSQL session-advisory-lock key used only as
// the fallback when the Redis leader lock is unavailable.
const LeaderAdvisoryKey int64 = 0x5343484544 // "SCHED"

type LeaderGuard struct {
	OwnerID   string
	Mode      string
	ExpiresAt time.Time
}

// GetLeaderGuard reads the single-row guard for the control-plane status page.
// A missing owner/expired lease is reported as inactive by the API layer.
func (s *Store) GetLeaderGuard(ctx context.Context) (LeaderGuard, error) {
	var g LeaderGuard
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(owner_id::text, ''), COALESCE(mode, ''),
		       COALESCE(expires_at, now() - interval '100 years')
		FROM leader_guard WHERE id=1`).Scan(&g.OwnerID, &g.Mode, &g.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeaderGuard{}, nil
	}
	return g, wrapErr(err)
}

// TryLeaderGuard acquires or keeps the single-row DB guard. The guard is the
// cross-mode fence: a Redis-lock leader and a PG-fallback leader cannot both
// hold it.
func (s *Store) TryLeaderGuard(ctx context.Context, owner uuid.UUID, mode string, ttl time.Duration) (bool, error) {
	var held bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO leader_guard (id, owner_id, mode, expires_at)
		VALUES (1, $1, $2, clock_timestamp() + make_interval(secs => $3))
		ON CONFLICT (id) DO UPDATE SET
			owner_id=EXCLUDED.owner_id, mode=EXCLUDED.mode, expires_at=EXCLUDED.expires_at
		WHERE leader_guard.owner_id IS NULL
		   OR leader_guard.owner_id = $1
		   OR leader_guard.expires_at < clock_timestamp()
		RETURNING owner_id = $1`,
		owner, mode, ttl.Seconds()).Scan(&held)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return held, wrapErr(err)
}

func (s *Store) RenewLeaderGuard(ctx context.Context, owner uuid.UUID, ttl time.Duration) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `
		UPDATE leader_guard
		SET expires_at=clock_timestamp() + make_interval(secs => $2)
		WHERE id=1 AND owner_id=$1
		RETURNING true`, owner, ttl.Seconds()).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, wrapErr(err)
}

func (s *Store) ReleaseLeaderGuard(ctx context.Context, owner uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE leader_guard SET owner_id=NULL, mode='', expires_at=NULL
		WHERE id=1 AND owner_id=$1`, owner)
	return wrapErr(err)
}

// LeaderSession holds a dedicated PostgreSQL connection with a session-level
// advisory lock. Closing/releasing the connection releases the lock.
type LeaderSession struct {
	conn *pgxpool.Conn
}

func (s *Store) AcquireLeaderSession(ctx context.Context) (*LeaderSession, bool, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, false, wrapErr(err)
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, LeaderAdvisoryKey).Scan(&ok); err != nil {
		conn.Release()
		return nil, false, wrapErr(err)
	}
	if !ok {
		conn.Release()
		return nil, false, nil
	}
	return &LeaderSession{conn: conn}, true, nil
}

func (l *LeaderSession) Alive(ctx context.Context) bool {
	if l == nil || l.conn == nil {
		return false
	}
	var one int
	return l.conn.QueryRow(ctx, `SELECT 1`).Scan(&one) == nil
}

func (l *LeaderSession) Release(ctx context.Context) {
	if l == nil || l.conn == nil {
		return
	}
	_, _ = l.conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, LeaderAdvisoryKey)
	l.conn.Release()
	l.conn = nil
}
