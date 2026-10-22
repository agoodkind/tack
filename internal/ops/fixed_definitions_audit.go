package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// stageFixedDefinitionAudit returns ctx with the operator command operation
// and the fixed definitions tool name. The replace transaction records the
// stored and corrected definition records under that operation.
func stageFixedDefinitionAudit(ctx context.Context, _, after *node.PropertyDef) (context.Context, error) {
	writeContext, err := audit.WithMutationTool(ctx, fixedDefinitionsCommand)
	if err != nil {
		slog.ErrorContext(ctx, "metadata.fixed_definition.audit_failed", slog.String("err", err.Error()),
			slog.String("property_definition_id", after.ID.String()))
		return ctx, fmt.Errorf("stage fixed definition audit for %s: %w", after.ID, err)
	}
	return writeContext, nil
}
