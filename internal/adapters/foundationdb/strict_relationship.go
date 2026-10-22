package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// AddMemberTarget implements node.MemberTargetAdder by calling add with
// true as the memberTarget argument.
func (s *RelationshipStore) AddMemberTarget(ctx context.Context, rel *node.Relationship) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.add_member_target")(&err)
	return s.add(ctx, rel, true)
}

// checkStrictRelationship returns nil when strict is false. When strict
// is true, it rejects a child_of relationship with an error that wraps
// node.ErrReservedParentRelation. The parent_id field sets the parent.
// For other relationships, it returns an error that wraps
// domain.ErrInvalidArgument when the source does not resolve to a node in
// rel.OrgID, the target resolves to a node in another organization, or the
// target does not resolve to a node and memberTarget is false.
func checkStrictRelationship(ctx context.Context, tr fdb.Transaction, strict bool, rel *node.Relationship, memberTarget bool) error {
	if !strict {
		return nil
	}
	if rel.RelationType == node.RelChildOf {
		wrapped := fmt.Errorf("add relationship from %s to %s: %w", rel.SourceID, rel.TargetID, node.ErrReservedParentRelation)
		slog.WarnContext(ctx, "relationship.add_refused", slog.String("err", wrapped.Error()), slog.String("source_id", rel.SourceID.String()))
		return loggedSearchError{err: wrapped}
	}
	source, err := readResultTarget(ctx, tr, rel.SourceID)
	if err != nil {
		return err
	}
	target, err := readResultTarget(ctx, tr, rel.TargetID)
	if err != nil {
		return err
	}
	if !source.Found || source.OrgID != rel.OrgID {
		return mutationRefusal(ctx, rel.SourceID, "the relationship source does not exist in the organization", domain.ErrInvalidArgument)
	}
	if target.Found && target.OrgID != rel.OrgID {
		return mutationRefusal(ctx, rel.SourceID, "the relationship target "+rel.TargetID.String()+" is a node in another organization", domain.ErrInvalidArgument)
	}
	if !target.Found && !memberTarget {
		return mutationRefusal(ctx, rel.SourceID, "the relationship target "+rel.TargetID.String()+" is neither a node nor a member of the organization", domain.ErrInvalidArgument)
	}
	return nil
}

// checkStrictRelationshipAdds returns nil when strict is false. When strict
// is true, it calls checkStrictRelationship with memberTarget false for each
// relationship in each Add list, except child_of, and returns the first error.
// This rejects targets that do not resolve to nodes. checkStrictRecord or
// checkStrictMutation checks child_of relationships.
func checkStrictRelationshipAdds(ctx context.Context, tr fdb.Transaction, strict bool, changesList []node.RelationshipChanges) error {
	if !strict {
		return nil
	}
	for _, changes := range changesList {
		for _, relationship := range changes.Add {
			if relationship.RelationType == node.RelChildOf {
				continue
			}
			if err := checkStrictRelationship(ctx, tr, strict, relationship, false); err != nil {
				return err
			}
		}
	}
	return nil
}
