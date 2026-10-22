package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/domain/org"
)

// seedMembership grants the seeded user the owner role.
func seedMembership(
	ctx context.Context,
	members *postgres.OrgMemberRepo,
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
