package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/domain/org"
)

// memberAdder requires AddMember to write the membership change and its audit
// event in one transaction.
type memberAdder interface {
	AddMember(ctx context.Context, m *org.Member) error
}

// seedMembership grants the seeded user the owner role.
func seedMembership(
	ctx context.Context,
	members memberAdder,
	orgID, userID uuid.UUID,
) error {
	member := &org.Member{
		ID: uuid.Nil, OrgID: orgID, UserID: userID,
		Role: seedOwnerRole, CreatedAt: time.Time{},
	}
	if err := members.AddMember(ctx, member); err != nil {
		slog.ErrorContext(ctx, "seed.add_member_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: add member: %w", err)
	}
	return nil
}

// seedOwnerRole is the org role the seeded user receives. It matches the
// numeric role the seed has always granted.
const seedOwnerRole = 20
