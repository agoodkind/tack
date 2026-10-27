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

// AddMemberTarget implements node.MemberTargetAdder. After activation, it
// accepts a target that does not resolve to a node.
func (s *RelationshipStore) AddMemberTarget(ctx context.Context, rel *node.Relationship) (err error) {
	defer telemetry.FDBOp(ctx, "store.relationship.add_member_target")(&err)
	return s.add(ctx, rel, true)
}

// checkStrictRelationship rejects every child_of relationship because the
// parent_id field sets the parent. A caller sets memberTarget only after it
// confirms in org_members that the target is a member of rel.OrgID.
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

// checkStrictRelationshipAdds skips child_of relationships, which
// checkStrictRecord or checkStrictMutation checks against parent_id.
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
