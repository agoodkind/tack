package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/module"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ModuleRepo struct {
	db *pgxpool.Pool
}

func NewModuleRepo(db *pgxpool.Pool) *ModuleRepo {
	return &ModuleRepo{db: db}
}

const moduleCols = `id, node_id, workspace_id, project_id, name, description, status,
	start_date, target_date, sort_order, created_by, updated_by, created_at, updated_at`

func (r *ModuleRepo) Create(ctx context.Context, m *module.Module) (*module.Module, error) {
	const q = `
		INSERT INTO modules (workspace_id, project_id, name, description, status,
		                     start_date, target_date, sort_order, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, node_id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q,
		m.WorkspaceID, m.ProjectID, m.Name, m.Description, m.Status,
		m.StartDate, m.TargetDate, m.SortOrder, m.CreatedBy, m.UpdatedBy,
	).Scan(&m.ID, &m.NodeID, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("module create: %w", pgErr(err))
	}
	return m, nil
}

func (r *ModuleRepo) GetByID(ctx context.Context, projectID, id uuid.UUID) (*module.Module, error) {
	q := `SELECT ` + moduleCols + ` FROM modules WHERE id=$1 AND project_id=$2`
	m := &module.Module{}
	err := r.db.QueryRow(ctx, q, id, projectID).Scan(
		&m.ID, &m.NodeID, &m.WorkspaceID, &m.ProjectID, &m.Name, &m.Description, &m.Status,
		&m.StartDate, &m.TargetDate, &m.SortOrder, &m.CreatedBy, &m.UpdatedBy, &m.CreatedAt, &m.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("module get: %w", err)
	}
	return m, nil
}

func (r *ModuleRepo) List(ctx context.Context, projectID uuid.UUID) ([]*module.Module, error) {
	q := `SELECT ` + moduleCols + ` FROM modules WHERE project_id=$1 ORDER BY sort_order ASC`
	rows, err := r.db.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("module list: %w", err)
	}
	defer rows.Close()
	var modules []*module.Module
	for rows.Next() {
		m := &module.Module{}
		if err := rows.Scan(
			&m.ID, &m.NodeID, &m.WorkspaceID, &m.ProjectID, &m.Name, &m.Description, &m.Status,
			&m.StartDate, &m.TargetDate, &m.SortOrder, &m.CreatedBy, &m.UpdatedBy, &m.CreatedAt, &m.UpdatedAt,
		); err != nil {
			return nil, err
		}
		modules = append(modules, m)
	}
	return modules, rows.Err()
}

func (r *ModuleRepo) Update(ctx context.Context, m *module.Module) (*module.Module, error) {
	const q = `
		UPDATE modules SET name=$1, description=$2, status=$3,
		    start_date=$4, target_date=$5, updated_by=$6, updated_at=now()
		WHERE id=$7
		RETURNING updated_at`
	err := r.db.QueryRow(ctx, q,
		m.Name, m.Description, m.Status,
		m.StartDate, m.TargetDate, m.UpdatedBy, m.ID,
	).Scan(&m.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("module update: %w", err)
	}
	return m, nil
}

func (r *ModuleRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM modules WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("module delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

