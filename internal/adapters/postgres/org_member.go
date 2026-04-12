package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/org"
)

// OrgMemberRepo handles org_members SQL operations (auth gate).
// org_members stays in SQL because it bridges SQL auth (users, api_tokens)
// with FDB entity data. This is the only SQL touch for org membership.
type OrgMemberRepo struct {
	pool *pgxpool.Pool
}

func NewOrgMemberRepo(pool *pgxpool.Pool) *OrgMemberRepo {
	return &OrgMemberRepo{pool: pool}
}

func (r *OrgMemberRepo) AddMember(ctx context.Context, m *org.Member) error {
	const q = `
		INSERT INTO org_members (org_id, user_id, role)
		VALUES ($1, $2, $3)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role
		RETURNING id, created_at`
	return r.pool.QueryRow(ctx, q, m.OrgID, m.UserID, m.Role).Scan(&m.ID, &m.CreatedAt)
}

func (r *OrgMemberRepo) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	const q = `DELETE FROM org_members WHERE org_id = $1 AND user_id = $2`
	tag, err := r.pool.Exec(ctx, q, orgID, userID)
	if err != nil {
		return fmt.Errorf("org remove member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *OrgMemberRepo) ListMembers(ctx context.Context, orgID uuid.UUID) ([]*org.Member, error) {
	const q = `SELECT id, org_id, user_id, role, created_at FROM org_members WHERE org_id = $1`
	rows, err := r.pool.Query(ctx, q, orgID)
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

// ListOrgIDsForUser returns all org UUIDs the user belongs to.
func (r *OrgMemberRepo) ListOrgIDsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	const q = `SELECT DISTINCT org_id FROM org_members WHERE user_id = $1`
	rows, err := r.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("list org ids for user: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan org id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
