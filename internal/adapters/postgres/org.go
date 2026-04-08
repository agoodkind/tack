package postgres

import (
	"context"
	"errors"
	"fmt"

	domain "github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/org"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type OrgRepo struct {
	db *pgxpool.Pool
}

func NewOrgRepo(db *pgxpool.Pool) *OrgRepo {
	return &OrgRepo{db: db}
}

func (r *OrgRepo) Create(ctx context.Context, o *org.Org) (*org.Org, error) {
	const q = `
		INSERT INTO orgs (name, slug)
		VALUES ($1, $2)
		RETURNING id, node_id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q, o.Name, o.Slug).
		Scan(&o.ID, &o.NodeID, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("org create: %w", pgErr(err))
	}
	return o, nil
}

func (r *OrgRepo) GetByID(ctx context.Context, id uuid.UUID) (*org.Org, error) {
	const q = `SELECT id, node_id, name, slug, created_at, updated_at FROM orgs WHERE id = $1`
	o := &org.Org{}
	err := r.db.QueryRow(ctx, q, id).
		Scan(&o.ID, &o.NodeID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("org get: %w", err)
	}
	return o, nil
}

func (r *OrgRepo) GetBySlug(ctx context.Context, slug string) (*org.Org, error) {
	const q = `SELECT id, node_id, name, slug, created_at, updated_at FROM orgs WHERE slug = $1`
	o := &org.Org{}
	err := r.db.QueryRow(ctx, q, slug).
		Scan(&o.ID, &o.NodeID, &o.Name, &o.Slug, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("org get by slug: %w", err)
	}
	return o, nil
}

func (r *OrgRepo) AddMember(ctx context.Context, m *org.Member) error {
	const q = `
		INSERT INTO org_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role
		RETURNING id, created_at`
	return r.db.QueryRow(ctx, q, m.OrgID, m.UserID, m.Role).
		Scan(&m.ID, &m.CreatedAt)
}

func (r *OrgRepo) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	const q = `DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`
	tag, err := r.db.Exec(ctx, q, orgID, userID)
	if err != nil {
		return fmt.Errorf("org remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *OrgRepo) ListMembers(ctx context.Context, orgID uuid.UUID) ([]*org.Member, error) {
	const q = `SELECT id, org_id, user_id, role, created_at FROM org_members WHERE org_id = $1`
	rows, err := r.db.Query(ctx, q, orgID)
	if err != nil {
		return nil, fmt.Errorf("org list members: %w", err)
	}
	defer rows.Close()
	var members []*org.Member
	for rows.Next() {
		m := &org.Member{}
		if err := rows.Scan(&m.ID, &m.OrgID, &m.UserID, &m.Role, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("org member scan: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}
