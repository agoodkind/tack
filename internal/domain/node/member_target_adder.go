package node

import "context"

// MemberTargetAdder adds a relationship to a target that is a member of
// the relationship organization and is not a node.
type MemberTargetAdder interface {
	// AddMemberTarget writes rel with the same transaction and checks as
	// RelationshipRepository.Add. When strict validation is active, it also
	// accepts a target that does not resolve to a node. The caller must confirm
	// that the target is a member of rel.OrgID before calling AddMemberTarget.
	AddMemberTarget(ctx context.Context, rel *Relationship) error
}
