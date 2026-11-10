package node

import "context"

// MemberTargetAdder adds a relationship with a target that belongs to the
// relationship organization as a member rather than a node.
type MemberTargetAdder interface {
	// AddMemberTarget writes rel with the transaction and checks that
	// RelationshipRepository.Add uses. When strict validation is active, it also
	// accepts a target that does not resolve to a node. The caller must confirm
	// that the target is a member of rel.OrgID before calling AddMemberTarget.
	AddMemberTarget(ctx context.Context, rel *Relationship) error
}
