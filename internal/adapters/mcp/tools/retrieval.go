package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

type getInput struct {
	NodeID string `json:"node_id"`
}

func getHandler(nodeType *node.NodeType, binding NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var input getInput
		if err := req.BindArguments(&input); err != nil {
			return recoverableError(err.Error()), nil
		}

		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}

		entryPoint, missing, err := requireEntryPointArg(ctx, args, binding)
		if missing != "" {
			return recoverableError(missing), nil
		}

		if err != nil {
			return classifyError(ctx, err), nil
		}

		nodeID, err := binding.Resolver.ResolveTypedNodeID(ctx, nodeType, input.NodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		view, err := binding.Reader.Get(ctx, nodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		if view == nil {
			return classifyError(ctx, domain.ErrNotFound), nil
		}

		if err := stampAuditNodeInEntryPoint(ctx, binding.Resolver, entryPoint, view); err != nil {
			return classifyError(ctx, err), nil
		}

		renderContext := newRenderCtxWithTypes(
			ctx,
			binding.Reader,
			binding.Users,
			binding.Resolver.typeIndex,
		)
		if err := renderContext.loadAssignees(binding.Relationships, view); err != nil {
			return classifyError(ctx, err), nil
		}

		return successText(renderNode(renderContext, view), ""), nil
	}
}
