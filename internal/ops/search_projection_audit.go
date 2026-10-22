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

// stageProjectionAudit reads the operation from ctx with audit.RequireMutationContext.
// It sets the operation's tool name to searchProjectionTool and returns a context
// with that operation.
func stageProjectionAudit(ctx context.Context, definition *node.PropertyDef) (context.Context, error) {
	writeContext, err := audit.WithMutationTool(ctx, searchProjectionTool)
	if err != nil {
		wrapped := fmt.Errorf("prepare the audit context for search projection of property definition %s: %w", definition.ID, err)
		slog.ErrorContext(ctx, "search.projection.audit_failed", slog.String("err", wrapped.Error()), slog.String("property_definition_id", definition.ID.String()))
		return ctx, node.LoggedProjectionError{Cause: wrapped}
	}
	return writeContext, nil
}
