package org

import (
	"context"

	"github.com/google/uuid"
)

// MemberRepository handles org membership (SQL auth gate).
// Org entity CRUD goes through the generic EntityRepository + NodeReader path.
type MemberRepository interface {
	AddMember(ctx context.Context, m *Member) error
	RemoveMember(ctx context.Context, orgID, userID uuid.UUID) error
	ListMembers(ctx context.Context, orgID uuid.UUID) ([]*Member, error)
	ListOrgIDsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}
