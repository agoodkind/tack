package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/org"
)

type memberValue struct {
	ID        uuid.UUID `json:"id"`
	OrgID     uuid.UUID `json:"org_id"`
	UserID    uuid.UUID `json:"user_id"`
	Role      int       `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// lockMember reads the membership for orgID and userID with FOR UPDATE. It
// returns false without an error when the membership does not exist.
func lockMember(ctx context.Context, tx pgx.Tx, orgID, userID uuid.UUID) (*memberValue, bool, error) {
	const q = `
		SELECT id, org_id, user_id, role, created_at FROM org_members
		WHERE org_id = $1 AND user_id = $2
		FOR UPDATE`
	stored := &memberValue{ID: uuid.Nil, OrgID: uuid.Nil, UserID: uuid.Nil, Role: 0, CreatedAt: time.Time{}}
	err := tx.QueryRow(ctx, q, orgID, userID).Scan(
		&stored.ID, &stored.OrgID, &stored.UserID, &stored.Role, &stored.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, pgErr(err)
	}
	return stored, true, nil
}

// saveMember inserts a membership when previous is nil. Otherwise, it updates
// the locked membership's role by previous.ID. It returns the stored membership.
func saveMember(ctx context.Context, tx pgx.Tx, previous *memberValue, m *org.Member) (*memberValue, error) {
	const insertQuery = `
		INSERT INTO org_members (id, org_id, user_id, role)
		VALUES ($1, $2, $3, $4)
		RETURNING id, org_id, user_id, role, created_at`
	const updateQuery = `
		UPDATE org_members SET role = $2 WHERE id = $1
		RETURNING id, org_id, user_id, role, created_at`
	var row pgx.Row
	if previous == nil {
		row = tx.QueryRow(ctx, insertQuery, uuid.Must(uuid.NewV7()), m.OrgID, m.UserID, m.Role)
	} else {
		row = tx.QueryRow(ctx, updateQuery, previous.ID, m.Role)
	}
	stored := &memberValue{ID: uuid.Nil, OrgID: uuid.Nil, UserID: uuid.Nil, Role: 0, CreatedAt: time.Time{}}
	if err := row.Scan(&stored.ID, &stored.OrgID, &stored.UserID, &stored.Role, &stored.CreatedAt); err != nil {
		return nil, pgErr(err)
	}
	return stored, nil
}

// writeMemberChange writes an audit change event with the membership's
// relationship target and old and new values in tx. It uses
// audit.VerbOrgMemberRoleSet when both values exist. audit.NewChange computes
// Changed from the values as sorted JSON pointers.
func writeMemberChange(ctx context.Context, tx pgx.Tx, operation audit.Event, before, after *memberValue) error {
	current := after
	if current == nil {
		current = before
	}
	target := MemberTarget(current.ID, current.OrgID, current.UserID)
	beforeValue, err := changeValue(ctx, before)
	if err != nil {
		return err
	}
	afterValue, err := changeValue(ctx, after)
	if err != nil {
		return err
	}
	if before != nil && after != nil {
		operation.Verb = string(audit.VerbOrgMemberRoleSet)
	}
	change, err := newChange(ctx, target, beforeValue, afterValue)
	if err != nil {
		return err
	}
	return WriteChanges(ctx, tx, operation, []audit.Change{change})
}
