package store

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrNotFound         = errors.New("not found")
	ErrConflict         = errors.New("conflict")
	ErrLeaseLost        = errors.New("lease lost")
	ErrLeaseUnavailable = errors.New("lease unavailable")
	ErrDependencyCycle  = errors.New("dependency cycle")
	ErrDependencyDepth  = errors.New("dependency depth exceeded")
	ErrDependencyFanout = errors.New("dependency fanout exceeded")
	ErrWorkerBusy       = errors.New("worker has running builds")
	ErrDependencyEdge   = errors.New("invalid dependency edge")
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return fmt.Errorf("store: %w", err)
}

func itoa(n int) string { return strconv.Itoa(n) }
