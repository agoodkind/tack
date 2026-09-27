package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

// ProjectionBackfillRepository scans property definitions in bounded pages
// and sets a search projection only on a definition that has none.
type ProjectionBackfillRepository interface {
	ScanProjections(ctx context.Context, cursor string) ([]*node.PropertyDef, string, error)
	SetProjectionIfNil(ctx context.Context, definition *node.PropertyDef, projection node.SearchProjection) (bool, error)
}

// RunSearchProjectionBackfill applies a sorted manifest. It never loads the
// whole manifest or every stored definition into memory.
func RunSearchProjectionBackfill(ctx context.Context, repository ProjectionBackfillRepository, manifest io.Reader, dryRun bool) (node.ProjectionBackfillResult, error) {
	reader, err := newProjectionManifestReader(ctx, manifest)
	if err != nil {
		return node.ProjectionBackfillResult{}, err
	}
	entry, err := reader.next(ctx)
	if err != nil {
		return node.ProjectionBackfillResult{}, err
	}
	var result node.ProjectionBackfillResult
	var cursor string
	for {
		definitions, next, scanErr := repository.ScanProjections(ctx, cursor)
		if scanErr != nil {
			wrapped := fmt.Errorf("scan property definitions after %q: %w", cursor, scanErr)
			return result, projectionFailure(ctx, "search.projection.scan_failed", wrapped)
		}
		entry, err = applyProjectionPage(ctx, repository, reader, definitions, entry, dryRun, &result)
		if err != nil {
			return result, err
		}
		if next == "" {
			break
		}
		if next == cursor {
			return result, projectionFailure(ctx, "search.projection.cursor_stalled", fmt.Errorf("property definition scan cursor did not advance"))
		}
		cursor = next
	}
	if entry != nil {
		return result, unknownProjectionEntry(ctx, repository, *entry)
	}
	if err := reader.finish(ctx); err != nil {
		return result, err
	}
	if result.Missing != 0 {
		return result, projectionFailure(ctx, "search.projection.missing", fmt.Errorf("manifest omits %d property definitions; first missing IDs: %v", result.Missing, result.MissingIDs))
	}
	return result, nil
}

func applyProjectionPage(ctx context.Context, repository ProjectionBackfillRepository, reader *projectionManifestReader, definitions []*node.PropertyDef, entry *node.ProjectionManifestEntry, dryRun bool, result *node.ProjectionBackfillResult) (*node.ProjectionManifestEntry, error) {
	for _, definition := range definitions {
		if definition == nil {
			return entry, projectionFailure(ctx, "search.projection.nil_definition", fmt.Errorf("scanned nil property definition"))
		}
		result.Scanned++
		if entry != nil && manifestEntryBeforeDefinition(*entry, definition) {
			return entry, unknownProjectionEntry(ctx, repository, *entry)
		}
		if entry == nil || entry.OrgID != definition.OrgID || entry.PropertyDefID != definition.ID {
			if definition.Search == nil {
				result.Missing++
				if len(result.MissingIDs) < 10 {
					result.MissingIDs = append(result.MissingIDs, node.ProjectionIdentity{OrgID: definition.OrgID, PropertyDefID: definition.ID})
				}
			}
			continue
		}
		if err := applyProjectionEntry(ctx, repository, definition, *entry, dryRun, result); err != nil {
			return entry, err
		}
		var err error
		entry, err = reader.next(ctx)
		if err != nil {
			return entry, err
		}
	}
	return entry, nil
}

func applyProjectionEntry(ctx context.Context, repository ProjectionBackfillRepository, definition *node.PropertyDef, entry node.ProjectionManifestEntry, dryRun bool, result *node.ProjectionBackfillResult) error {
	if definition.Search != nil {
		if !searchProjectionsEqual(*definition.Search, entry.Search) {
			return projectionFailure(ctx, "search.projection.conflict", fmt.Errorf("property definition %s already has a different projection", definition.ID))
		}
		result.Unchanged++
		return nil
	}
	if dryRun {
		result.Changed++
		if len(result.PlannedIDs) < 10 {
			result.PlannedIDs = append(result.PlannedIDs, node.ProjectionIdentity{OrgID: definition.OrgID, PropertyDefID: definition.ID})
		}
		return nil
	}
	writeContext, err := stageProjectionAudit(ctx, definition)
	if err != nil {
		return err
	}
	changed, err := repository.SetProjectionIfNil(writeContext, definition, entry.Search)
	if err != nil {
		wrapped := fmt.Errorf("set projection for property definition %s: %w", definition.ID, err)
		return projectionFailure(ctx, "search.projection.set_failed", wrapped)
	}
	if changed {
		result.Changed++
		if len(result.AppliedIDs) < 10 {
			result.AppliedIDs = append(result.AppliedIDs, node.ProjectionIdentity{OrgID: definition.OrgID, PropertyDefID: definition.ID})
		}
	} else {
		result.Unchanged++
	}
	return nil
}

func manifestEntryBeforeDefinition(entry node.ProjectionManifestEntry, definition *node.PropertyDef) bool {
	if entry.OrgID != definition.OrgID {
		return entry.OrgID.String() < definition.OrgID.String()
	}
	return entry.PropertyDefID.String() < definition.ID.String()
}

func unknownProjectionEntry(ctx context.Context, repository ProjectionBackfillRepository, entry node.ProjectionManifestEntry) error {
	var cursor string
	for {
		definitions, next, err := repository.ScanProjections(ctx, cursor)
		if err != nil {
			wrapped := fmt.Errorf("check organization for property definition %s: %w", entry.PropertyDefID, err)
			return projectionFailure(ctx, "search.projection.scan_failed", wrapped)
		}
		for _, definition := range definitions {
			if definition.ID == entry.PropertyDefID && definition.OrgID != entry.OrgID {
				return projectionFailure(ctx, "search.projection.org_mismatch", fmt.Errorf("property definition %s belongs to org %s, not manifest org %s", entry.PropertyDefID, definition.OrgID, entry.OrgID))
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return projectionFailure(ctx, "search.projection.unknown", fmt.Errorf("manifest property definition %s in org %s does not exist", entry.PropertyDefID, entry.OrgID))
}

func searchProjectionsEqual(left, right node.SearchProjection) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func projectionFailure(ctx context.Context, event string, wrapped error) error {
	var logged node.LoggedProjectionError
	if !errors.As(wrapped, &logged) {
		telemetry.L(ctx).ErrorContext(ctx, event, slog.String("err", wrapped.Error()))
	}
	return wrapped
}
