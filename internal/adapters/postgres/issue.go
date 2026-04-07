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
			name, description_html, description_stripped,
			priority, sequence_id, sort_order,
			start_date, target_date, is_draft,
			external_source, external_id,
			created_by, updated_by
		) VALUES (
			$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18
		)
		RETURNING id, created_at, updated_at`

	row := r.db.QueryRow(ctx, q,
		i.WorkspaceID, i.ProjectID, i.ParentID, i.StateID, i.EpicID,
		i.Name, i.DescriptionHTML, i.DescriptionStripped,
		string(i.Priority), i.SequenceID, i.SortOrder,
		i.StartDate, i.TargetDate, i.IsDraft,
		i.ExternalSource, i.ExternalID,
		i.CreatedBy, i.UpdatedBy,
	)
	if err := row.Scan(&i.ID, &i.CreatedAt, &i.UpdatedAt); err != nil {
		return nil, fmt.Errorf("issue create: %w", pgErr(err))
	}
	return i, nil
}

func (r *IssueRepo) GetByID(ctx context.Context, workspaceID, projectID, id uuid.UUID) (*issue.Issue, error) {
	const q = `
		SELECT id, workspace_id, project_id, parent_id, state_id, epic_id,
		       name, description_html, description_stripped,
		       priority, sequence_id, sort_order,
		       start_date, target_date, completed_at, archived_at, is_draft,
		       external_source, external_id,
		       created_by, updated_by, created_at, updated_at
		FROM issues
		WHERE id = $1 AND workspace_id = $2 AND project_id = $3 AND deleted_at IS NULL`

	i := &issue.Issue{}
	var priority string
	err := r.db.QueryRow(ctx, q, id, workspaceID, projectID).Scan(
		&i.ID, &i.WorkspaceID, &i.ProjectID, &i.ParentID, &i.StateID, &i.EpicID,
		&i.Name, &i.DescriptionHTML, &i.DescriptionStripped,
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
	// TODO: build dynamic query from filter; placeholder returns all for the project.
	const q = `
		SELECT id, workspace_id, project_id, parent_id, state_id, epic_id,
		       name, description_html, description_stripped,
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
			&i.ID, &i.WorkspaceID, &i.ProjectID, &i.ParentID, &i.StateID, &i.EpicID,
			&i.Name, &i.DescriptionHTML, &i.DescriptionStripped,
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
			name = $1, description_html = $2, description_stripped = $3,
			priority = $4, state_id = $5, epic_id = $6, parent_id = $7,
			start_date = $8, target_date = $9, is_draft = $10,
			updated_by = $11, updated_at = now()
		WHERE id = $12 AND deleted_at IS NULL
		RETURNING updated_at`

	err := r.db.QueryRow(ctx, q,
		i.Name, i.DescriptionHTML, i.DescriptionStripped,
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

func (r *IssueRepo) SetEpic(ctx context.Context, issueID uuid.UUID, epicID *uuid.UUID) error {
	const q = `UPDATE issues SET epic_id = $1, updated_at = now() WHERE id = $2 AND deleted_at IS NULL`
	_, err := r.db.Exec(ctx, q, epicID, issueID)
	return err
}

func (r *IssueRepo) Search(ctx context.Context, workspaceID uuid.UUID, q string, filter issue.ListFilter) ([]*issue.Issue, int, error) {
	const sq = `
		SELECT id, workspace_id, project_id, parent_id, state_id, epic_id,
		       name, description_html, description_stripped,
		       priority, sequence_id, sort_order,
		       start_date, target_date, completed_at, archived_at, is_draft,
		       external_source, external_id,
		       created_by, updated_by, created_at, updated_at
		FROM issues
		WHERE workspace_id = $1
		  AND deleted_at IS NULL
		  AND search_vector @@ websearch_to_tsquery('english', $2)
		ORDER BY ts_rank(search_vector, websearch_to_tsquery('english', $2)) DESC
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
			&i.ID, &i.WorkspaceID, &i.ProjectID, &i.ParentID, &i.StateID, &i.EpicID,
			&i.Name, &i.DescriptionHTML, &i.DescriptionStripped,
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
