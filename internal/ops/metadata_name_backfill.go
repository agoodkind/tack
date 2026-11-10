package ops

import (
	"context"
	"fmt"
	"log/slog"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/telemetry"
)

// NameIndexBackfillStore reads one bounded page of the metadata records of
// every organization and finds the records without a name index entry. When
// write is true, it writes the missing entries of that page.
type NameIndexBackfillStore interface {
	BackfillNameIndex(ctx context.Context, cursor string, write bool) (fdbadapter.NameIndexPage, error)
}

// NameIndexCount reports one metadata name index. Missing counts the records
// without an index entry. An executed run writes those entries.
type NameIndexCount struct {
	Scanned int `json:"scanned"`
	Missing int `json:"missing"`
}

// MetadataNameBackfillResult reports the type-key index of node types and
// the property-name index of property definitions.
type MetadataNameBackfillResult struct {
	NodeTypes           NameIndexCount `json:"node_types"`
	PropertyDefinitions NameIndexCount `json:"property_definitions"`
}

// RunMetadataNameBackfill finds the node types and property definitions of
// every organization that lack a name index entry. Search reads node types
// by type key and property definitions by name through these indexes, and
// metadata written before the indexes existed has no entries. Unless dryRun
// is true, it writes the missing entries.
func RunMetadataNameBackfill(ctx context.Context, nodeTypes, propertyDefinitions NameIndexBackfillStore, dryRun bool) (MetadataNameBackfillResult, error) {
	var result MetadataNameBackfillResult
	var err error
	result.NodeTypes, err = backfillNameIndex(ctx, "node type", nodeTypes, dryRun)
	if err != nil {
		return result, err
	}
	result.PropertyDefinitions, err = backfillNameIndex(ctx, "property definition", propertyDefinitions, dryRun)
	if err != nil {
		return result, err
	}
	telemetry.L(ctx).InfoContext(ctx, "metadata_name_index.backfilled",
		slog.Bool("dry_run", dryRun),
		slog.Int("node_types_scanned", result.NodeTypes.Scanned),
		slog.Int("node_types_missing", result.NodeTypes.Missing),
		slog.Int("property_definitions_scanned", result.PropertyDefinitions.Scanned),
		slog.Int("property_definitions_missing", result.PropertyDefinitions.Missing))
	return result, nil
}

func backfillNameIndex(ctx context.Context, family string, store NameIndexBackfillStore, dryRun bool) (NameIndexCount, error) {
	var count NameIndexCount
	cursor := ""
	for {
		page, err := store.BackfillNameIndex(ctx, cursor, !dryRun)
		if err != nil {
			wrapped := fmt.Errorf("backfill %s name index after cursor %q: %w", family, cursor, err)
			slog.ErrorContext(ctx, "metadata_name_index.backfill_failed", slog.String("err", wrapped.Error()), slog.String("family", family))
			return count, wrapped
		}
		count.Scanned += page.Scanned
		count.Missing += page.Missing
		if page.Next == "" {
			return count, nil
		}
		cursor = page.Next
	}
}
