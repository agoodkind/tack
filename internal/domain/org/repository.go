package org

import (
	"context"

	"github.com/google/uuid"
)

// MemberRepository reads organization membership for SQL authorization.
// Generic EntityRepository and NodeReader operations manage organizations.
type MemberRepository interface {
	ListMembers(ctx context.Context, orgID uuid.UUID) ([]*Member, error)
	ListOrgIDsForUser(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
}
