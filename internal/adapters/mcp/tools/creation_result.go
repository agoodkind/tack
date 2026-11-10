package tools

import (
	"context"
	"encoding/json"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func renderCreateResult(
	ctx context.Context,
	binding NodeTypeBinding,
	result *service.CreateResult,
	name string,
	rawProps map[string]json.RawMessage,
	defs []*node.PropertyDef,
) *mcpmcp.CallToolResult {
	instr := ""
	if result.Existed {
		instr = "This create matched an existing operation. No new write was performed."
		target := nodeTarget(result.View.NodeType, result.View.ID)
		noteToolResult(
			ctx,
			audit.OutcomeDetails{
				Outcome:        audit.OutcomeCompleted,
				FailureCode:    "",
				FailureMessage: "",
				CommittedSteps: nil,
			},
			idempotentMatchReason,
			&target,
		)
	}

	renderContext := newRenderCtxWithTypes(
		ctx,
		binding.Reader,
		binding.Users,
		binding.Resolver.typeIndex,
	)
	setCreateAuditEntity(ctx, result.View, renderContext)
	changes := fieldChanges{Shown: nil, Total: 0}
	if !result.Existed {
		changes = describeFieldChanges(
			renderContext,
			writeChanges{
				Before:        nil,
				After:         result.View,
				Name:          &name,
				Props:         rawProps,
				ReferenceKeys: referenceKeysOf(defs),
				targets:       nil,
			},
		)
	}

	return successText(
		renderWriteConfirmation(renderContext, "Created", result.View, changes),
		instr,
	)
}
