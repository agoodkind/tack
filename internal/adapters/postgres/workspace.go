package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/workspace"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type WorkspaceRepo struct {
	db *pgxpool.Pool
}

func NewWorkspaceRepo(db *pgxpool.Pool) *WorkspaceRepo {
	return &WorkspaceRepo{db: db}
}

func (r *WorkspaceRepo) Create(ctx context.Context, w *workspace.Workspace) (*workspace.Workspace, error) {
	const q = `
		INSERT INTO workspaces (org_id, name, slug)
		VALUES ($1, $2, $3)
		RETURNING id, node_id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q, w.OrgID, w.Name, w.Slug).
		Scan(&w.ID, &w.NodeID, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("workspace create: %w", pgErr(err))
	}
	return w, nil
}

func (r *WorkspaceRepo) GetByID(ctx context.Context, id uuid.UUID) (*workspace.Workspace, error) {
	const q = `SELECT id, node_id, org_id, name, slug, created_at, updated_at FROM workspaces WHERE id = $1`
	w := &workspace.Workspace{}
	err := r.db.QueryRow(ctx, q, id).
		Scan(&w.ID, &w.NodeID, &w.OrgID, &w.Name, &w.Slug, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("workspace get: %w", err)
	}
	return w, nil
}

func (r *WorkspaceRepo) GetBySlug(ctx context.Context, slug string) (*workspace.Workspace, error) {
	const q = `SELECT id, node_id, org_id, name, slug, created_at, updated_at FROM workspaces WHERE slug = $1`
	w := &workspace.Workspace{}
	err := r.db.QueryRow(ctx, q, slug).
		Scan(&w.ID, &w.NodeID, &w.OrgID, &w.Name, &w.Slug, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("workspace get by slug: %w", err)
	}
	return w, nil
}

func (r *WorkspaceRepo) List(ctx context.Context, orgID uuid.UUID) ([]*workspace.Workspace, error) {
	const q = `SELECT id, node_id, org_id, name, slug, created_at, updated_at FROM workspaces WHERE org_id = $1`
	rows, err := r.db.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("workspace list: %w", err)
	}
	defer rows.Close()
	var workspaces []*workspace.Workspace
	for rows.Next() {
		w := &workspace.Workspace{}
		if err := rows.Scan(&w.ID, &w.NodeID, &w.OrgID, &w.Name, &w.Slug, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, fmt.Errorf("workspace scan: %w", err)
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, rows.Err()
}

func (r *WorkspaceRepo) ListForUser(ctx context.Context, userID uuid.UUID) ([]*workspace.Workspace, error) {
	const q = `
		SELECT DISTINCT w.id, w.node_id, w.org_id, w.name, w.slug, w.created_at, w.updated_at
		FROM workspaces w
		JOIN org_members om ON om.org_id = w.org_id
		WHERE om.user_id = $1
		ORDER BY w.name`
	rows, err := r.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("workspace list for user: %w", err)
	}
	defer rows.Close()
	var workspaces []*workspace.Workspace
	for rows.Next() {
		w := &workspace.Workspace{}
		if err := rows.Scan(&w.ID, &w.NodeID, &w.OrgID, &w.Name, &w.Slug, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return nil, fmt.Errorf("workspace scan: %w", err)
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, rows.Err()
}
