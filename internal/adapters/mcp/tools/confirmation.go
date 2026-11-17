package tools

import (
	"context"
	"encoding/json"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

const auditEntityNode = "node"

func renderCreatedNode(
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
	}

	renderContext := newRenderCtxWithTypes(
		ctx,
		binding.Reader,
		binding.Users,
		binding.Resolver.typeIndex,
	)
	audit.SetAuditEntity(
		ctx,
		audit.Entity{
			Type:       auditEntityNode,
			NodeType:   result.View.NodeType,
			ID:         result.View.ID,
			Identifier: identifierFor(result.View, renderContext),
			Name:       result.View.Name,
		},
	)
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
