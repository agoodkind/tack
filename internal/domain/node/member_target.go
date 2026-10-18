package node

import (
	"context"

	"github.com/google/uuid"
)

type memberTargetKey struct{}

// memberTarget is one relationship target that the node service found in
// org_members for an organization.
type memberTarget struct {
	orgID    uuid.UUID
	targetID uuid.UUID
}

// WithMemberTarget returns a context derived from ctx that records targetID
// as a member of orgID. The node service records the membership after reading
// org_members. The relationship store checks the record during its write
// transaction.
func WithMemberTarget(ctx context.Context, orgID, targetID uuid.UUID) context.Context {
	return context.WithValue(ctx, memberTargetKey{}, memberTarget{orgID: orgID, targetID: targetID})
}

// IsMemberTarget reports whether ctx records targetID as a member of orgID.
func IsMemberTarget(ctx context.Context, orgID, targetID uuid.UUID) bool {
	recorded, ok := ctx.Value(memberTargetKey{}).(memberTarget)
	return ok && recorded.orgID == orgID && recorded.targetID == targetID
}
