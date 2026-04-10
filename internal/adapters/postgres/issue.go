package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type IssueRepo struct {
	db *pgxpool.Pool
}

func NewIssueRepo(db *pgxpool.Pool) *IssueRepo {
	return &IssueRepo{db: db}
}

func (r *IssueRepo) Create(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	const q = `
		INSERT INTO issues (
			workspace_id, project_id, parent_id, state_id, epic_id,
			name, description,
			priority, sequence_id, sort_order,
			start_date, target_date, is_draft,
			external_source, external_id,
			created_by, updated_by
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17
		)
		RETURNING id, node_id, created_at, updated_at`

	row := r.db.QueryRow(ctx, q,
		i.WorkspaceID, i.ProjectID, i.ParentID, i.StateID, i.EpicID,
		i.Name, i.Description,
		string(i.Priority), i.SequenceID, i.SortOrder,
		i.StartDate, i.TargetDate, i.IsDraft,
		i.ExternalSource, i.ExternalID,
		i.CreatedBy, i.UpdatedBy,
	)
	if err := row.Scan(&i.ID, &i.NodeID, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, fmt.Errorf("issue create: %w", pgErr(err))
	}
	return i, nil
}

func (r *IssueRepo) GetByID(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*issue.Issue, error) {
	const q = `
		SELECT id, node_id, workspace_id, project_id, parent_id, state_id, epic_id,
		       name, description,
		       priority, sequence_id, sort_order,
		       start_date, target_date, completed_at, archived_at, is_draft,
		       external_source, external_id,
		       created_by, updated_by, created_at, updated_at
		FROM issues
		WHERE id = $1 AND workspace_id = $2 AND project_id = $3 AND deleted_at IS NULL`

	i := &issue.Issue{}
	var priority string
	err := r.db.QueryRow(ctx, q, id, workspaceID, projectID).Scan(
		&i.ID, &i.NodeID, &i.WorkspaceID, &i.ProjectID, &i.ParentID, &i.StateID, &i.EpicID,
		&i.Name, &i.Description,
		&priority, &i.SequenceID, &i.SortOrder,
		&i.StartDate, &i.TargetDate, &i.CompletedAt, &i.ArchivedAt, &i.IsDraft,
		&i.ExternalSource, &i.ExternalID,
		&i.CreatedBy, &i.UpdatedBy, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("issue get: %w", err)
	}
	i.Priority = issue.Priority(priority)
	return i, nil
}

func (r *IssueRepo) List(ctx context.Context, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	// description is intentionally omitted — large TEXT blobs don't belong in list results.
	// TODO: build dynamic query from filter; placeholder returns all for the project.
	const q = `
		SELECT id, node_id, workspace_id, project_id, parent_id, state_id, epic_id,
		       name,
		       priority, sequence_id, sort_order,
		       start_date, target_date, completed_at, archived_at, is_draft,
		       external_source, external_id,
		       created_by, updated_by, created_at, updated_at
		FROM issues
		WHERE workspace_id = $1 AND project_id = $2 AND deleted_at IS NULL
		ORDER BY sort_order ASC, created_at DESC`

	rows, err := r.db.Query(ctx, q, filter.WorkspaceID, filter.ProjectID)
	if err != nil {
		return nil, 0, fmt.Errorf("issue list: %w", err)
	}
	defer rows.Close()

	var issues []*issue.Issue
	for rows.Next() {
		i := &issue.Issue{}
		var priority string
		if err := rows.Scan(
			&i.ID, &i.NodeID, &i.WorkspaceID, &i.ProjectID, &i.ParentID, &i.StateID, &i.EpicID,
			&i.Name,
			&priority, &i.SequenceID, &i.SortOrder,
			&i.StartDate, &i.TargetDate, &i.CompletedAt, &i.ArchivedAt, &i.IsDraft,
			&i.ExternalSource, &i.ExternalID,
			&i.CreatedBy, &i.UpdatedBy, &i.CreatedAt, &i.UpdatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("issue scan: %w", err)
		}
		i.Priority = issue.Priority(priority)
		issues = append(issues, i)
	}
	return issues, len(issues), rows.Err()
}

func (r *IssueRepo) Update(ctx context.Context, i *issue.Issue) (*issue.Issue, error) {
	const q = `
		UPDATE issues SET
			name = $1, description = $2,
			priority = $3, state_id = $4, epic_id = $5, parent_id = $6,
			start_date = $7, target_date = $8, is_draft = $9,
			updated_by = $10, updated_at = now()
		WHERE id = $11 AND deleted_at IS NULL
		RETURNING updated_at`

	err := r.db.QueryRow(ctx, q,
		i.Name, i.Description,
		string(i.Priority), i.StateID, i.EpicID, i.ParentID,
		i.StartDate, i.TargetDate, i.IsDraft,
		i.UpdatedBy, i.ID,
	).Scan(&i.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("issue update: %w", err)
	}
	return i, nil
}

func (r *IssueRepo) Delete(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE issues SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`
	tag, err := r.db.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("issue delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *IssueRepo) Move(ctx context.Context, issueID, targetProjectID uuid.UUID, newSequenceID int, actorID uuid.UUID) (*issue.Issue, error) {
	const q = `
		UPDATE issues SET
			project_id  = $1,
			sequence_id = $2,
			state_id    = NULL,
			updated_by  = $3,
			updated_at  = now()
		WHERE id = $4 AND deleted_at IS NULL
		RETURNING id, node_id, workspace_id, project_id, parent_id, state_id, epic_id,
		          name, description,
		          priority, sequence_id, sort_order,
		          start_date, target_date, completed_at, archived_at, is_draft,
		          external_source, external_id,
		          created_by, updated_by, created_at, updated_at`

	i := &issue.Issue{}
	var priority string
	err := r.db.QueryRow(ctx, q, targetProjectID, newSequenceID, actorID, issueID).Scan(
		&i.ID, &i.NodeID, &i.WorkspaceID, &i.ProjectID, &i.ParentID, &i.StateID, &i.EpicID,
		&i.Name, &i.Description,
		&priority, &i.SequenceID, &i.SortOrder,
		&i.StartDate, &i.TargetDate, &i.CompletedAt, &i.ArchivedAt, &i.IsDraft,
		&i.ExternalSource, &i.ExternalID,
		&i.CreatedBy, &i.UpdatedBy, &i.CreatedAt, &i.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("issue move: %w", err)
	}
	i.Priority = issue.Priority(priority)
	return i, nil
}

func (r *IssueRepo) BulkUpdate(ctx context.Context, patch issue.BulkUpdatePatch) (int, error) {
	if len(patch.IssueIDs) == 0 {
		return 0, nil
	}
	var priorityStr *string
	if patch.Priority != nil {
		s := string(*patch.Priority)
		priorityStr = &s
	}
	const q = `
		UPDATE issues SET
			state_id   = CASE WHEN $1::uuid IS NOT NULL THEN $1::uuid   ELSE state_id END,
			priority   = CASE WHEN $2::text IS NOT NULL THEN $2::text   ELSE priority END,
			epic_id    = CASE WHEN $3::boolean           THEN $4::uuid  ELSE epic_id  END,
			updated_by = $5,
			updated_at = now()
		WHERE id = ANY($6::uuid[]) AND deleted_at IS NULL`

	tag, err := r.db.Exec(ctx, q,
		patch.StateID, priorityStr,
		patch.SetEpicID, patch.EpicID,
		patch.ActorID, patch.IssueIDs,
	)
	if err != nil {
		return 0, fmt.Errorf("issue bulk update: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (r *IssueRepo) BulkDelete(ctx context.Context, issueIDs []uuid.UUID) ([]uuid.UUID, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}
	const q = `
		UPDATE issues SET deleted_at = now()
		WHERE id = ANY($1::uuid[]) AND deleted_at IS NULL
		RETURNING node_id`
	rows, err := r.db.Query(ctx, q, issueIDs)
	if err != nil {
		return nil, fmt.Errorf("issue bulk delete: %w", err)
	}
	defer rows.Close()
	var nodeIDs []uuid.UUID
	for rows.Next() {
		var nid uuid.UUID
		if err := rows.Scan(&nid); err != nil {
			return nil, err
		}
		nodeIDs = append(nodeIDs, nid)
	}
	return nodeIDs, rows.Err()
}

func (r *IssueRepo) SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error {
	const q = `UPDATE issues SET epic_id = $1, updated_at = now() WHERE id = $2 AND deleted_at IS NULL`
	_, err := r.db.Exec(ctx, q, epicID, issueID)
	return err
}

func (r *IssueRepo) Search(ctx context.Context, workspaceID uuid.UUID, q string, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	// Full-text search is delegated to Meilisearch. This fallback does a simple
	// ILIKE on name for cases where Meilisearch is unavailable.
	const sq = `
		SELECT id, node_id, workspace_id, project_id, parent_id, state_id, epic_id,
		       name,
		       priority, sequence_id, sort_order,
		       start_date, target_date, completed_at, archived_at, is_draft,
		       external_source, external_id,
		       created_by, updated_by, created_at, updated_at
		FROM issues
		WHERE workspace_id = $1
		  AND deleted_at IS NULL
		  AND name ILIKE '%' || $2 || '%'
		ORDER BY created_at DESC
		LIMIT 50`

	rows, err := r.db.Query(ctx, sq, workspaceID, q)
	if err != nil {
		return nil, 0, fmt.Errorf("issue search: %w", err)
	}
	defer rows.Close()

	var issues []*issue.Issue
	for rows.Next() {
		i := &issue.Issue{}
		var priority string
		if err := rows.Scan(
			&i.ID, &i.NodeID, &i.WorkspaceID, &i.ProjectID, &i.ParentID, &i.StateID, &i.EpicID,
			&i.Name,
			&priority, &i.SequenceID, &i.SortOrder,
			&i.StartDate, &i.TargetDate, &i.CompletedAt, &i.ArchivedAt, &i.IsDraft,
			&i.ExternalSource, &i.ExternalID,
			&i.CreatedBy, &i.UpdatedBy, &i.CreatedAt, &i.UpdatedAt,
		); err != nil {
			return nil, 0, err
		}
		i.Priority = issue.Priority(priority)
		issues = append(issues, i)
	}
	return issues, len(issues), rows.Err()
}
