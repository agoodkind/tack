package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/project"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ProjectRepo struct {
	db *pgxpool.Pool
}

func NewProjectRepo(db *pgxpool.Pool) *ProjectRepo {
	return &ProjectRepo{db: db}
}

func (r *ProjectRepo) Create(ctx context.Context, p *project.Project) (*project.Project, error) {
	const q = `
		INSERT INTO projects (workspace_id, name, identifier, description, network, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, node_id, created_at, updated_at`

	err := r.db.QueryRow(ctx, q,
		p.WorkspaceID, p.Name, p.Identifier, p.Description, p.Network, p.CreatedBy, p.UpdatedBy,
	).Scan(&p.ID, &p.NodeID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("project create: %w", pgErr(err))
	}
	return p, nil
}

func (r *ProjectRepo) GetByID(ctx context.Context, workspaceID, id uuid.UUID) (*project.Project, error) {
	const q = `
		SELECT id, workspace_id, name, identifier, description, network,
		       default_state_id, created_by, updated_by, created_at, updated_at
		FROM projects
		WHERE id = $1 AND workspace_id = $2`

	p := &project.Project{}
	err := r.db.QueryRow(ctx, q, id, workspaceID).Scan(
		&p.ID, &p.WorkspaceID, &p.Name, &p.Identifier, &p.Description, &p.Network,
		&p.DefaultStateID, &p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("project get: %w", err)
	}
	return p, nil
}

func (r *ProjectRepo) GetByIdentifier(ctx context.Context, workspaceID uuid.UUID, identifier string) (*project.Project, error) {
	const q = `
		SELECT id, workspace_id, name, identifier, description, network,
		       default_state_id, created_by, updated_by, created_at, updated_at
		FROM projects
		WHERE workspace_id = $1 AND identifier = $2`

	p := &project.Project{}
	err := r.db.QueryRow(ctx, q, workspaceID, identifier).Scan(
		&p.ID, &p.WorkspaceID, &p.Name, &p.Identifier, &p.Description, &p.Network,
		&p.DefaultStateID, &p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("project get by identifier: %w", err)
	}
	return p, nil
}

func (r *ProjectRepo) List(ctx context.Context, workspaceID uuid.UUID) ([]*project.Project, error) {
	const q = `
		SELECT id, workspace_id, name, identifier, description, network,
		       default_state_id, created_by, updated_by, created_at, updated_at
		FROM projects
		WHERE workspace_id = $1
		ORDER BY name ASC`

	rows, err := r.db.Query(ctx, q, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("project list workspace %s: %w", workspaceID, err)
	}
	defer rows.Close()

	var projects []*project.Project
	for rows.Next() {
		p := &project.Project{}
		if err := rows.Scan(
			&p.ID, &p.WorkspaceID, &p.Name, &p.Identifier, &p.Description, &p.Network,
			&p.DefaultStateID, &p.CreatedBy, &p.UpdatedBy, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("project list scan workspace %s: %w", workspaceID, err)
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (r *ProjectRepo) Update(ctx context.Context, p *project.Project) (*project.Project, error) {
	const q = `
		UPDATE projects SET
			name = $1, description = $2, network = $3,
			identifier = $4, default_state_id = $5,
			updated_by = $6, updated_at = now()
		WHERE id = $7
		RETURNING updated_at`

	err := r.db.QueryRow(ctx, q,
		p.Name, p.Description, p.Network,
		p.Identifier, p.DefaultStateID,
		p.UpdatedBy, p.ID,
	).Scan(&p.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("project update: %w", err)
	}
	return p, nil
}

func (r *ProjectRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete project %s: %w", id, err)
	}
	return nil
}

// AllocateSequenceID atomically increments the per-project sequence counter and
// returns the allocated number. No trigger, no race condition.
func (r *ProjectRepo) AllocateSequenceID(ctx context.Context, projectID uuid.UUID, entityType string) (int, error) {
	const q = `
		INSERT INTO project_sequences (project_id, entity_type, next_seq)
		VALUES ($1, $2, 2)
		ON CONFLICT (project_id, entity_type)
		DO UPDATE SET next_seq = project_sequences.next_seq + 1
		RETURNING next_seq - 1`

	var seq int
	if err := r.db.QueryRow(ctx, q, projectID, entityType).Scan(&seq); err != nil {
		return 0, fmt.Errorf("allocate seq: %w", err)
	}
	return seq, nil
}
