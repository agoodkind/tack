package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkStrictRelationship returns nil when strict is false. When strict is
// true, it rejects child_of relationships with an error that wraps
// node.ErrReservedParentRelation. The parent_id field sets the parent.
// For other relationships, it returns an error that wraps
// domain.ErrInvalidArgument when the source does not resolve to a node in
// rel.OrgID, the target resolves to a node in another organization, or the
// target does not resolve to a node and ctx does not record it as a member
// of rel.OrgID.
func checkStrictRelationship(ctx context.Context, tr fdb.Transaction, strict bool, rel *node.Relationship) error {
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
	if !target.Found && !node.IsMemberTarget(ctx, rel.OrgID, rel.TargetID) {
		return mutationRefusal(ctx, rel.SourceID, "the relationship target "+rel.TargetID.String()+" is neither a node nor a member of the organization", domain.ErrInvalidArgument)
	}
	return nil
}
