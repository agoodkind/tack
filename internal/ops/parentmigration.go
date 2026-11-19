package ops

import (
	"context"
	"fmt"
	"log/slog"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/telemetry"
)

// ParentRelationshipBackfillResult counts changes to legacy parent records.
// A dry run counts proposed changes. An executed run counts committed changes.
type ParentRelationshipBackfillResult struct {
	Scanned            int `json:"scanned"`
	Changed            int `json:"changed"`
	RelationshipsAdded int `json:"relationships_added"`
	DefinitionsRemoved int `json:"definitions_removed"`
}

// RunParentRelationshipBackfill creates missing child_of relationships.
//
// It validates every node and read view before writing.
// Writers must remain stopped during validation and relationship creation.
//
// A dry run reports proposed changes without modifying data:
//
//	result, err := RunParentRelationshipBackfill(ctx, nodes, definitions, true)
func RunParentRelationshipBackfill(
	ctx context.Context,
	nodes *fdbadapter.NodeStore,
	_ *fdbadapter.PropertyDefStore,
	dryRun bool,
) (ParentRelationshipBackfillResult, error) {
	result, err := backfillParentRelationshipNodes(ctx, nodes, false)
	if err != nil {
		return result, err
	}

	if err := preflightParentRelationshipViews(ctx, nodes); err != nil {
		return result, err
	}

	if !dryRun {
		result, err = backfillParentRelationshipNodes(ctx, nodes, true)
		if err != nil {
			return result, err
		}
	}

	telemetry.L(ctx).InfoContext(
		ctx,
		"parent_relationships.backfilled",
		slog.Bool("dry_run", dryRun),
		slog.Int("scanned", result.Scanned),
		slog.Int("changed", result.Changed),
		slog.Int("relationships_added", result.RelationshipsAdded),
		slog.Int("definitions_removed", result.DefinitionsRemoved),
	)

	return result, nil
}

func backfillParentRelationshipNodes(
	ctx context.Context,
	nodes *fdbadapter.NodeStore,
	write bool,
) (ParentRelationshipBackfillResult, error) {
	result := ParentRelationshipBackfillResult{
		Scanned:            0,
		Changed:            0,
		RelationshipsAdded: 0,
		DefinitionsRemoved: 0,
	}
	cursor := ""

	for {
		page, err := nodes.BackfillParentRelationships(ctx, cursor, write)
		if err != nil {
			return result, parentRelationshipFailure(
				ctx,
				fmt.Sprintf("migrate parent relationships after cursor %q", cursor),
				err,
			)
		}

		result.Scanned += page.Scanned
		result.Changed += page.Changed
		result.RelationshipsAdded += page.Added
		if page.Next == "" {
			return result, nil
		}

		cursor = page.Next
	}
}

func parentRelationshipFailure(ctx context.Context, operation string, failure error) error {
	wrapped := fmt.Errorf("%s: %w", operation, failure)
	telemetry.L(ctx).ErrorContext(
		ctx,
		"parent_relationships.backfill_failed",
		slog.String("err", wrapped.Error()),
	)

	return wrapped
}
