package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
)

type deleteInput struct {
	NodeID string `json:"node_id"`
}

func deleteHandler(nodeType *node.NodeType, binding NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var input deleteInput
		if err := req.BindArguments(&input); err != nil {
			return recoverableError(err.Error()), nil
		}

		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
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

		existing, err := binding.Reader.Get(ctx, nodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		if existing == nil {
			return classifyError(ctx, domain.ErrNotFound), nil
		}

		if err := stampAuditNodeInEntryPoint(
			ctx,
			binding.Resolver,
			entryPoint,
			existing,
		); err != nil {
			return classifyError(ctx, err), nil
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
				NodeType:   existing.NodeType,
				ID:         existing.ID,
				Identifier: identifierFor(existing, renderContext),
				Name:       existing.Name,
			},
		)
		// Refuse before the write, not only in the response.
		if !isAuthorized(ctx) {
			return permissionDenied(ctx), nil
		}

		result, err := binding.NodeSvc.Delete(ctx, nodeID, userID)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		return successText(
			renderDeletedNode(renderContext, existing, clock.Now().UTC(), result),
			"",
		), nil
	}
}
