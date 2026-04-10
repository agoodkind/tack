package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/label"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LabelRepo struct {
	db *pgxpool.Pool
}

func NewLabelRepo(db *pgxpool.Pool) *LabelRepo {
	return &LabelRepo{db: db}
}

func (r *LabelRepo) Create(ctx context.Context, l *label.Label) (*label.Label, error) {
	const q = `
		INSERT INTO labels (workspace_id, project_id, name, color, sort_order)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q, l.WorkspaceID, l.ProjectID, l.Name, l.Color, l.SortOrder).
		Scan(&l.ID, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("label create: %w", pgErr(err))
	}
	return l, nil
}

func (r *LabelRepo) GetByID(ctx context.Context, id uuid.UUID) (*label.Label, error) {
	const q = `
		SELECT id, workspace_id, project_id, name, color, sort_order, created_at, updated_at
		FROM labels WHERE id = $1`
	l := &label.Label{}
	err := r.db.QueryRow(ctx, q, id).
		Scan(&l.ID, &l.WorkspaceID, &l.ProjectID, &l.Name, &l.Color, &l.SortOrder, &l.CreatedAt, &l.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("label get: %w", err)
	}
	return l, nil
}

func (r *LabelRepo) List(ctx context.Context, workspaceID uuid.UUID, projectID *uuid.UUID) ([]*label.Label, error) {
	const q = `
		SELECT id, workspace_id, project_id, name, color, sort_order, created_at, updated_at
		FROM labels
		WHERE workspace_id = $1 AND (project_id = $2 OR project_id IS NULL)
		ORDER BY sort_order ASC`
	rows, err := r.db.Query(ctx, q, workspaceID, projectID)
	if err != nil {
		return nil, fmt.Errorf("label list: %w", err)
	}
	defer rows.Close()
	var labels []*label.Label
	for rows.Next() {
		l := &label.Label{}
		if err := rows.Scan(&l.ID, &l.WorkspaceID, &l.ProjectID, &l.Name, &l.Color, &l.SortOrder, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

func (r *LabelRepo) Update(ctx context.Context, l *label.Label) (*label.Label, error) {
	const q = `
		UPDATE labels SET name = $1, color = $2, sort_order = $3, updated_at = now()
		WHERE id = $4
		RETURNING updated_at`
	err := r.db.QueryRow(ctx, q, l.Name, l.Color, l.SortOrder, l.ID).Scan(&l.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("label update: %w", err)
	}
	return l, nil
}

func (r *LabelRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM labels WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("label delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}
