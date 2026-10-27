package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
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

// AddMember locks any existing membership and inserts a membership or replaces
// its role with an audit change in the same transaction. An unchanged role
// only copies the stored ID and CreatedAt into m and does not write a change
// event.
func (r *OrgMemberRepo) AddMember(ctx context.Context, m *org.Member) error {
	operation, err := changeOperation(ctx, audit.VerbOrgMemberAdd, m.OrgID)
	if err != nil {
		return err
	}
	target := MemberTarget(uuid.Nil, m.OrgID, m.UserID)
	err = changeTx(ctx, r.pool, operation, target, func(tx pgx.Tx) error {
		previous, found, err := lockMember(ctx, tx, m.OrgID, m.UserID)
		if err != nil {
			return err
		}
		if found && previous.Role == m.Role {
			m.ID, m.CreatedAt = previous.ID, previous.CreatedAt
			return nil
		}
		stored, err := saveMember(ctx, tx, previous, m)
		if err != nil {
			return err
		}
		m.ID, m.CreatedAt = stored.ID, stored.CreatedAt
		return writeMemberChange(ctx, tx, operation, previous, stored)
	})
	if err != nil {
		slog.ErrorContext(ctx, "org.member_add_failed", slog.String("user_id", m.UserID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("add org member %s: %w", m.UserID, err)
	}
	return nil
}

// RemoveMember deletes a membership and records its old value in the same
// transaction. It returns an error wrapping domain.ErrNotFound when the
// membership does not exist.
func (r *OrgMemberRepo) RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error {
	const q = `
		DELETE FROM org_members WHERE org_id = $1 AND user_id = $2
		RETURNING id, org_id, user_id, role, created_at`
	operation, err := changeOperation(ctx, audit.VerbOrgMemberRemove, orgID)
	if err != nil {
		return err
	}
	target := MemberTarget(uuid.Nil, orgID, userID)
	err = changeTx(ctx, r.pool, operation, target, func(tx pgx.Tx) error {
		removed := &memberValue{ID: uuid.Nil, OrgID: uuid.Nil, UserID: uuid.Nil, Role: 0, CreatedAt: time.Time{}}
		if err := tx.QueryRow(ctx, q, orgID, userID).Scan(
			&removed.ID, &removed.OrgID, &removed.UserID, &removed.Role, &removed.CreatedAt,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrNotFound
			}
			return pgErr(err)
		}
		return writeMemberChange(ctx, tx, operation, removed, nil)
	})
	if err != nil {
		slog.ErrorContext(ctx, "org.member_remove_failed", slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("remove org member %s: %w", userID, err)
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
