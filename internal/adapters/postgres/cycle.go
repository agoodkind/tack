package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/cycle"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CycleRepo struct {
	db *pgxpool.Pool
}

func NewCycleRepo(db *pgxpool.Pool) *CycleRepo {
	return &CycleRepo{db: db}
}

const cycleCols = `id, node_id, workspace_id, project_id, name, description,
	start_date, end_date, sort_order, completed_points,
	created_by, updated_by, created_at, updated_at`

func (r *CycleRepo) Create(ctx context.Context, c *cycle.Cycle) (*cycle.Cycle, error) {
	const q = `
		INSERT INTO cycles (workspace_id, project_id, name, description,
		                    start_date, end_date, sort_order, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, node_id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q,
		c.WorkspaceID, c.ProjectID, c.Name, c.Description,
		c.StartDate, c.EndDate, c.SortOrder, c.CreatedBy, c.UpdatedBy,
	).Scan(&c.ID, &c.NodeID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("cycle create: %w", pgErr(err))
	}
	return c, nil
}

func (r *CycleRepo) GetByID(ctx context.Context, projectID, id uuid.UUID) (*cycle.Cycle, error) {
	q := `SELECT ` + cycleCols + ` FROM cycles WHERE id=$1 AND project_id=$2`
	c := &cycle.Cycle{}
	err := r.db.QueryRow(ctx, q, id, projectID).Scan(
		&c.ID, &c.NodeID, &c.WorkspaceID, &c.ProjectID, &c.Name, &c.Description,
		&c.StartDate, &c.EndDate, &c.SortOrder, &c.CompletedPoints,
		&c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("cycle get: %w", err)
	}
	return c, nil
}

func (r *CycleRepo) List(ctx context.Context, projectID uuid.UUID) ([]*cycle.Cycle, error) {
	q := `SELECT ` + cycleCols + ` FROM cycles WHERE project_id=$1 ORDER BY start_date ASC NULLS LAST, created_at DESC`
	rows, err := r.db.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("cycle list: %w", err)
	}
	defer rows.Close()
	var cycles []*cycle.Cycle
	for rows.Next() {
		c := &cycle.Cycle{}
		if err := rows.Scan(
			&c.ID, &c.NodeID, &c.WorkspaceID, &c.ProjectID, &c.Name, &c.Description,
			&c.StartDate, &c.EndDate, &c.SortOrder, &c.CompletedPoints,
			&c.CreatedBy, &c.UpdatedBy, &c.CreatedAt, &c.UpdatedAt,
		); err != nil {
			return nil, err
		}
		cycles = append(cycles, c)
	}
	return cycles, rows.Err()
}

func (r *CycleRepo) Update(ctx context.Context, c *cycle.Cycle) (*cycle.Cycle, error) {
	const q = `
		UPDATE cycles SET name=$1, description=$2, start_date=$3, end_date=$4,
		    updated_by=$5, updated_at=now()
		WHERE id=$6
		RETURNING updated_at`
	err := r.db.QueryRow(ctx, q,
		c.Name, c.Description, c.StartDate, c.EndDate, c.UpdatedBy, c.ID,
	).Scan(&c.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("cycle update: %w", err)
	}
	return c, nil
}

func (r *CycleRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `DELETE FROM cycles WHERE id=$1`, id)
	if err != nil {
		return fmt.Errorf("cycle delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *CycleRepo) AddIssues(ctx context.Context, cycleID uuid.UUID, issueIDs []uuid.UUID) error {
	for _, issueID := range issueIDs {
		_, err := r.db.Exec(ctx,
			`INSERT INTO cycle_issues (cycle_id, issue_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`,
			cycleID, issueID,
		)
		if err != nil {
			return fmt.Errorf("cycle add issue: %w", err)
		}
	}
	return nil
}

func (r *CycleRepo) RemoveIssue(ctx context.Context, cycleID, issueID uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM cycle_issues WHERE cycle_id=$1 AND issue_id=$2`, cycleID, issueID)
	return err
}

func (r *CycleRepo) ListIssueIDs(ctx context.Context, cycleID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `SELECT issue_id FROM cycle_issues WHERE cycle_id=$1`, cycleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
