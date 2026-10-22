package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// searchProjectionTool is the tool name that each projection correction
// records in its change event.
const searchProjectionTool = "ops.backfill.once-search-projections"

// stageProjectionAudit returns ctx with the operator command operation and
// the search projection tool name. The projection transaction records the
// stored and corrected definition records under that operation.
func stageProjectionAudit(ctx context.Context, definition *node.PropertyDef) (context.Context, error) {
	writeContext, err := audit.WithMutationTool(ctx, searchProjectionTool)
	if err != nil {
		wrapped := fmt.Errorf("apply search projection for %s: %w", definition.ID, err)
		slog.ErrorContext(ctx, "search.projection.audit_failed", slog.String("err", wrapped.Error()), slog.String("property_definition_id", definition.ID.String()))
		return ctx, node.LoggedProjectionError{Cause: wrapped}
	}
	return writeContext, nil
}
