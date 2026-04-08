package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/epic"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type EpicRepo struct {
	db *pgxpool.Pool
}

func NewEpicRepo(db *pgxpool.Pool) *EpicRepo {
	return &EpicRepo{db: db}
}

const epicCols = `id, node_id, workspace_id, project_id, parent_id, state_id,
	name, priority, sequence_id, sort_order,
	start_date, target_date, completed_at, archived_at, is_draft,
	created_by, updated_by, created_at, updated_at`

func scanEpic(row interface {
	Scan(...any) error
}) (*epic.Epic, error) {
	e := &epic.Epic{}
	var priority string
	err := row.Scan(
		&e.ID, &e.NodeID, &e.WorkspaceID, &e.ProjectID, &e.ParentID, &e.StateID,
		&e.Name, &priority, &e.SequenceID, &e.SortOrder,
		&e.StartDate, &e.TargetDate, &e.CompletedAt, &e.ArchivedAt, &e.IsDraft,
		&e.CreatedBy, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	e.Priority = issue.Priority(priority)
	return e, nil
}

func (r *EpicRepo) Create(ctx context.Context, e *epic.Epic) (*epic.Epic, error) {
	const q = `
		INSERT INTO epics (workspace_id, project_id, parent_id, state_id, name, priority,
		                   sequence_id, sort_order, start_date, target_date, is_draft, created_by, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		RETURNING id, node_id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q,
		e.WorkspaceID, e.ProjectID, e.ParentID, e.StateID, e.Name, string(e.Priority),
		e.SequenceID, e.SortOrder, e.StartDate, e.TargetDate, e.IsDraft, e.CreatedBy, e.UpdatedBy,
	).Scan(&e.ID, &e.NodeID, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("epic create: %w", pgErr(err))
	}
	return e, nil
}

func (r *EpicRepo) GetByID(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*epic.Epic, error) {
	q := `SELECT ` + epicCols + ` FROM epics WHERE id=$1 AND workspace_id=$2 AND project_id=$3 AND deleted_at IS NULL`
	e, err := scanEpic(r.db.QueryRow(ctx, q, id, workspaceID, projectID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("epic get: %w", err)
	}
	return e, nil
}

func (r *EpicRepo) List(ctx context.Context, workspaceID, projectID uuid.UUID) ([]*epic.Epic, int, error) {
	q := `SELECT ` + epicCols + ` FROM epics WHERE workspace_id=$1 AND project_id=$2 AND deleted_at IS NULL ORDER BY sort_order ASC`
	rows, err := r.db.Query(ctx, q, workspaceID, projectID)
	if err != nil {
		return nil, 0, fmt.Errorf("epic list: %w", err)
	}
	defer rows.Close()
	var epics []*epic.Epic
	for rows.Next() {
		e, err := scanEpic(rows)
		if err != nil {
			return nil, 0, err
		}
		epics = append(epics, e)
	}
	return epics, len(epics), rows.Err()
}

func (r *EpicRepo) Update(ctx context.Context, e *epic.Epic) (*epic.Epic, error) {
	const q = `
		UPDATE epics SET name=$1, priority=$2, state_id=$3, parent_id=$4,
		    start_date=$5, target_date=$6, is_draft=$7, updated_by=$8, updated_at=now()
		WHERE id=$9 AND deleted_at IS NULL
		RETURNING updated_at`
	err := r.db.QueryRow(ctx, q,
		e.Name, string(e.Priority), e.StateID, e.ParentID,
		e.StartDate, e.TargetDate, e.IsDraft, e.UpdatedBy, e.ID,
	).Scan(&e.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("epic update: %w", err)
	}
	return e, nil
}

func (r *EpicRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.db.Exec(ctx, `UPDATE epics SET deleted_at=now() WHERE id=$1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("epic delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *EpicRepo) ListIssueIDs(ctx context.Context, epicID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.db.Query(ctx, `SELECT id FROM issues WHERE epic_id=$1 AND deleted_at IS NULL`, epicID)
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
