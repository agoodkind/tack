package ops

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// RunFixedNodeTypeCorrection uses store to correct the CanLiveUnder lists
// of stored built-in node types in every organization. When dryRun is false,
// each correction writes one node_type.update change event with the operator
// command operation from ctx. A dry run does not write.
func RunFixedNodeTypeCorrection(ctx context.Context, store service.FixedNodeTypeStore, dryRun bool) (service.FixedNodeTypeResult, error) {
	result, err := service.CorrectFixedNodeTypes(ctx, store, stageFixedNodeTypeAudit, dryRun)
	if err != nil {
		wrapped := fmt.Errorf("run fixed node type correction: %w", err)
		slog.ErrorContext(ctx, "metadata.fixed_node_type.run_failed", slog.String("err", wrapped.Error()))
		return result, wrapped
	}
	return result, nil
}

// stageFixedNodeTypeAudit returns ctx with the operator command operation and
// the fixed definitions tool name. The replace transaction records the stored
// and corrected node type records under that operation.
func stageFixedNodeTypeAudit(ctx context.Context, _, after *node.NodeType) (context.Context, error) {
	writeContext, err := audit.WithMutationTool(ctx, fixedDefinitionsCommand)
	if err != nil {
		slog.ErrorContext(ctx, "metadata.fixed_node_type.audit_failed", slog.String("err", err.Error()),
			slog.String("node_type_id", after.ID.String()))
		return ctx, fmt.Errorf("stage fixed node type audit for %s: %w", after.ID, err)
	}
	return writeContext, nil
}
