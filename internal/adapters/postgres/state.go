package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/state"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type StateRepo struct {
	db *pgxpool.Pool
}

func NewStateRepo(db *pgxpool.Pool) *StateRepo {
	return &StateRepo{db: db}
}

func (r *StateRepo) Create(ctx context.Context, s *state.State) (*state.State, error) {
	const q = `
		INSERT INTO states (project_id, name, group_name, color, sort_order)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q, s.ProjectID, s.Name, string(s.GroupName), s.Color, s.SortOrder).
		Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("state create: %w", pgErr(err))
	}
	return s, nil
}

func (r *StateRepo) GetByID(ctx context.Context, projectID, id uuid.UUID) (*state.State, error) {
	const q = `
		SELECT id, project_id, name, group_name, color, sort_order, created_at, updated_at
		FROM states WHERE id = $1 AND project_id = $2`
	s := &state.State{}
	var gn string
	err := r.db.QueryRow(ctx, q, id, projectID).
		Scan(&s.ID, &s.ProjectID, &s.Name, &gn, &s.Color, &s.SortOrder, &s.CreatedAt, &s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("state get: %w", err)
	}
	s.GroupName = state.GroupName(gn)
	return s, nil
}

func (r *StateRepo) List(ctx context.Context, projectID uuid.UUID) ([]*state.State, error) {
	const q = `
		SELECT id, project_id, name, group_name, color, sort_order, created_at, updated_at
		FROM states WHERE project_id = $1 ORDER BY sort_order ASC`
	rows, err := r.db.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("state list: %w", err)
	}
	defer rows.Close()
	var states []*state.State
	for rows.Next() {
		s := &state.State{}
		var gn string
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.Name, &gn, &s.Color, &s.SortOrder, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.GroupName = state.GroupName(gn)
		states = append(states, s)
	}
	return states, rows.Err()
}

func (r *StateRepo) Update(ctx context.Context, s *state.State) (*state.State, error) {
	const q = `
		UPDATE states SET name = $1, group_name = $2, color = $3, sort_order = $4, updated_at = now()
		WHERE id = $5 AND project_id = $6
		RETURNING updated_at`
	err := r.db.QueryRow(ctx, q, s.Name, string(s.GroupName), s.Color, s.SortOrder, s.ID, s.ProjectID).
		Scan(&s.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("state update: %w", err)
	}
	return s, nil
}

func (r *StateRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM states WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("state delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
