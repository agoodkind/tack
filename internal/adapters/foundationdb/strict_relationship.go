package foundationdb

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

// checkStrictRelationship returns nil when strict is false. Otherwise, it
// refuses a child_of relationship with node.ErrReservedParentRelation,
// because parent_id sets the parent. It also refuses a relationship when its
// source or target does not exist in the relationship's organization, with an
// error that wraps domain.ErrInvalidArgument.
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
	if !target.Found || target.OrgID != rel.OrgID {
		return mutationRefusal(ctx, rel.SourceID, "the relationship target "+rel.TargetID.String()+" does not exist in the organization", domain.ErrInvalidArgument)
	}
	return nil
}
