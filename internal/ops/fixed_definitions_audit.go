package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
)

// stageFixedDefinitionAudit reads the operation from ctx with audit.RequireMutationContext.
// It sets the operation's tool name to fixedDefinitionsCommand and returns a context
// with that operation.
func stageFixedDefinitionAudit(ctx context.Context, _, after *node.PropertyDef) (context.Context, error) {
	writeContext, err := audit.WithMutationTool(ctx, fixedDefinitionsCommand)
	if err != nil {
		slog.ErrorContext(ctx, "metadata.fixed_definition.audit_failed", slog.String("err", err.Error()),
			slog.String("property_definition_id", after.ID.String()))
		return ctx, fmt.Errorf("prepare the audit context for property definition %s: %w", after.ID, err)
	}
	return writeContext, nil
}
