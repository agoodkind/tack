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

const memberRoleField = "role"

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

func upsertMember(ctx context.Context, tx pgx.Tx, m *org.Member) (*memberValue, error) {
	const q = `
		INSERT INTO org_members (id, org_id, user_id, role)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role
		RETURNING id, org_id, user_id, role, created_at`
	stored := &memberValue{ID: uuid.Nil, OrgID: uuid.Nil, UserID: uuid.Nil, Role: 0, CreatedAt: time.Time{}}
	err := tx.QueryRow(ctx, q, uuid.Must(uuid.NewV7()), m.OrgID, m.UserID, m.Role).Scan(
		&stored.ID, &stored.OrgID, &stored.UserID, &stored.Role, &stored.CreatedAt,
	)
	if err != nil {
		return nil, pgErr(err)
	}
	return stored, nil
}

// writeMemberChange writes an audit change event with the membership's
// relationship target and old and new values in tx. It marks role as the
// changed field when both values exist.
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
	var changed []string
	if before != nil && after != nil {
		changed = []string{memberRoleField}
	}
	return WriteChanges(ctx, tx, operation, []audit.Change{{
		EventID: uuid.Must(uuid.NewV7()), Target: target,
		Before: beforeValue, After: afterValue, Changed: changed,
	}})
}
