package store

import (
	"context"

	"github.com/google/uuid"

	"scheduler-platform/internal/domain"
)

func (s *Store) CreateProject(ctx context.Context, name string, description *string) (domain.Project, error) {
	var p domain.Project
	err := s.pool.QueryRow(ctx, `
		INSERT INTO project (name, description) VALUES ($1,$2)
		RETURNING id, name, description, created_at`, name, description,
	).Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt)
	if isUniqueViolation(err) {
		return p, ErrConflict
	}
	if err != nil {
		return p, wrapErr(err)
	}
	return p, nil
}

func (s *Store) GetProject(ctx context.Context, id uuid.UUID) (domain.Project, error) {
	var p domain.Project
	err := s.pool.QueryRow(ctx, `SELECT id, name, description, created_at FROM project WHERE id=$1`, id).
		Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt)
	if err != nil {
		return p, wrapErr(err)
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, description, created_at FROM project ORDER BY name`)
	if err != nil {
		return nil, wrapErr(err)
	}
	defer rows.Close()
	out := []domain.Project{}
	for rows.Next() {
		var p domain.Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.CreatedAt); err != nil {
			return nil, wrapErr(err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
