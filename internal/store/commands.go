package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"scheduler-platform/internal/domain"
)

type EnqueueCommandInput struct {
	WorkerID uuid.UUID
	RunID    *uuid.UUID
	Attempt  *int
	Seq      *int
	Kind     string
	Payload  any
	TTL      time.Duration
}

func (s *Store) EnqueueCommand(ctx context.Context, in EnqueueCommandInput) (domain.WorkerCommand, error) {
	if in.TTL <= 0 {
		in.TTL = 60 * time.Second
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil {
		return domain.WorkerCommand{}, err
	}
	var runID any
	if in.RunID != nil {
		runID = *in.RunID
	}
	var attempt, seq any
	if in.Attempt != nil {
		attempt = *in.Attempt
	}
	if in.Seq != nil {
		seq = *in.Seq
	}
	var out domain.WorkerCommand
	err = scanCommand(s.pool.QueryRow(ctx, `
		INSERT INTO worker_command (worker_id, run_id, attempt, seq, kind, payload, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,clock_timestamp() + make_interval(secs => $7))
		RETURNING `+commandColumns,
		in.WorkerID, runID, attempt, seq, in.Kind, payload, in.TTL.Seconds(),
	), &out)
	if err != nil {
		return out, wrapErr(err)
	}
	return out, nil
}

// PollCommands waits up to wait for one command for the worker. SKIP LOCKED
// ensures concurrent long-polls cannot deliver the same command twice.
func (s *Store) PollCommands(ctx context.Context, workerID uuid.UUID, wait time.Duration) (*domain.WorkerCommand, error) {
	if wait <= 0 {
		wait = 30 * time.Second
	}
	deadline := time.Now().Add(wait)
	for {
		cmd, err := s.tryPollCommand(ctx, workerID)
		if err != nil {
			return nil, err
		}
		if cmd != nil {
			return cmd, nil
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (s *Store) tryPollCommand(ctx context.Context, workerID uuid.UUID) (*domain.WorkerCommand, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer tx.Rollback(ctx)

	var cmd domain.WorkerCommand
	err = scanCommand(tx.QueryRow(ctx, `
		SELECT `+commandColumns+`
		FROM worker_command
		WHERE worker_id=$1 AND state='PENDING' AND expires_at > clock_timestamp()
		ORDER BY created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, workerID), &cmd)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapErr(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE worker_command SET state='DELIVERED', delivered_at=clock_timestamp() WHERE id=$1`, cmd.ID); err != nil {
		return nil, wrapErr(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, wrapErr(err)
	}
	cmd.State = "DELIVERED"
	now := time.Now().UTC()
	cmd.DeliveredAt = &now
	return &cmd, nil
}

type CompleteCommandInput struct {
	ID     uuid.UUID
	State  string // DONE | FAILED
	Result any
	Error  string
}

func (s *Store) CompleteCommand(ctx context.Context, in CompleteCommandInput) error {
	if in.State != "DONE" && in.State != "FAILED" {
		return errors.New("command state must be DONE or FAILED")
	}
	result, err := json.Marshal(in.Result)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE worker_command SET state=$2, result=$3, last_error=NULLIF($4,''), finished_at=clock_timestamp()
		WHERE id=$1 AND state IN ('PENDING','DELIVERED')`, in.ID, in.State, result, in.Error)
	if err != nil {
		return wrapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetCommand(ctx context.Context, id uuid.UUID) (domain.WorkerCommand, error) {
	var cmd domain.WorkerCommand
	err := scanCommand(s.pool.QueryRow(ctx, `SELECT `+commandColumns+` FROM worker_command WHERE id=$1`, id), &cmd)
	if err != nil {
		return cmd, wrapErr(err)
	}
	return cmd, nil
}

func (s *Store) ExpireCommands(ctx context.Context, limit int) (int64, error) {
	if limit <= 0 {
		limit = 1000
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE worker_command SET state='EXPIRED', finished_at=clock_timestamp()
		WHERE id IN (
			SELECT id FROM worker_command
			WHERE state IN ('PENDING','DELIVERED') AND expires_at <= clock_timestamp()
			LIMIT $1
		)`, limit)
	if err != nil {
		return 0, wrapErr(err)
	}
	return tag.RowsAffected(), nil
}

func scanCommand(row pgx.Row, c *domain.WorkerCommand) error {
	var runID uuid.NullUUID
	var attempt, seq sql.NullInt64
	var result []byte
	var lastError sql.NullString
	var deliveredAt, finishedAt sql.NullTime
	if err := row.Scan(&c.ID, &c.WorkerID, &runID, &attempt, &seq, &c.Kind, &c.Payload,
		&c.State, &result, &lastError, &c.ExpiresAt, &deliveredAt, &finishedAt, &c.CreatedAt); err != nil {
		return err
	}
	if runID.Valid {
		v := runID.UUID
		c.RunID = &v
	}
	if attempt.Valid {
		v := int(attempt.Int64)
		c.Attempt = &v
	}
	if seq.Valid {
		v := int(seq.Int64)
		c.Seq = &v
	}
	c.Result = result
	if lastError.Valid {
		c.LastError = &lastError.String
	}
	if deliveredAt.Valid {
		c.DeliveredAt = &deliveredAt.Time
	}
	if finishedAt.Valid {
		c.FinishedAt = &finishedAt.Time
	}
	return nil
}

const commandColumns = `id, worker_id, run_id, attempt, seq, kind, payload, state,
	result, last_error, expires_at, delivered_at, finished_at, created_at`
