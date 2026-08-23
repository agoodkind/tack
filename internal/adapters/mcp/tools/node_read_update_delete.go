package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

type getInput struct {
	NodeID string `json:"node_id"`
}

func getHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in getInput
		if err := req.BindArguments(&in); err != nil {
			return recoverableError(err.Error()), nil
		}
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}
		entryPoint, missing, err := requireEntryPointArg(ctx, args, b)
		if missing != "" {
			return recoverableError(missing), nil
		}
		if err != nil {
			return classifyError(ctx, err), nil
		}
		id, err := b.Resolver.ResolveTypedNodeID(ctx, nt, in.NodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		view, err := b.Reader.Get(ctx, id)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		if view == nil {
			return classifyError(ctx, domain.ErrNotFound), nil
		}
		if err := stampAuditNodeInEntryPoint(ctx, b.Resolver, entryPoint, view); err != nil {
			return classifyError(ctx, err), nil
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		return successText(renderNode(rc, view), ""), nil
	}
}

// updateInput omits Properties on purpose. BindArguments cannot reliably
// preserve a stringified JSON payload through its mapstructure-style decoder
// across different MCP transports. The handler reads properties straight
// from the typed argMap and routes through parseProps. See TACK-165.
type updateInput struct {
	NodeID string  `json:"node_id"`
	Name   *string `json:"name,omitempty"`
}

func updateHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in updateInput
		if err := req.BindArguments(&in); err != nil {
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
		entryPoint, missing, err := requireEntryPointArg(ctx, args, b)
		if missing != "" {
			return recoverableError(missing), nil
		}
		if err != nil {
			return classifyError(ctx, err), nil
		}
		id, err := b.Resolver.ResolveTypedNodeID(ctx, nt, in.NodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		rawProps, propsResult := parseToolProps(args["properties"])
		if propsResult != nil {
			return propsResult, nil
		}
		existing, err := b.Reader.Get(ctx, id)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		if existing == nil {
			return classifyError(ctx, domain.ErrNotFound), nil
		}
		if err := stampAuditNodeInEntryPoint(ctx, b.Resolver, entryPoint, existing); err != nil {
			return classifyError(ctx, err), nil
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		audit.SetAuditEntity(ctx, audit.Entity{
			Type:       "node",
			NodeType:   existing.NodeType,
			ID:         existing.ID,
			Identifier: identifierFor(existing, rc),
			Name:       existing.Name,
		})
		rawProps, err = normalizeUpdateProps(ctx, b, nt, existing, rawProps)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		view, err := b.NodeSvc.Update(ctx, service.UpdateInput{
			NodeID:              id,
			Name:                in.Name,
			Props:               rawProps,
			RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
			ActorID:             userID,
		})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		return successText(renderNode(rc, view), ""), nil
	}
}

type deleteInput struct {
	NodeID string `json:"node_id"`
}

func deleteHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in deleteInput
		if err := req.BindArguments(&in); err != nil {
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
		entryPoint, missing, err := requireEntryPointArg(ctx, args, b)
		if missing != "" {
			return recoverableError(missing), nil
		}
		if err != nil {
			return classifyError(ctx, err), nil
		}
		id, err := b.Resolver.ResolveTypedNodeID(ctx, nt, in.NodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		existing, err := b.Reader.Get(ctx, id)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		if existing == nil {
			return classifyError(ctx, domain.ErrNotFound), nil
		}
		if err := stampAuditNodeInEntryPoint(ctx, b.Resolver, entryPoint, existing); err != nil {
			return classifyError(ctx, err), nil
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		audit.SetAuditEntity(ctx, audit.Entity{
			Type:       "node",
			NodeType:   existing.NodeType,
			ID:         existing.ID,
			Identifier: identifierFor(existing, rc),
			Name:       existing.Name,
		})
		if err := b.NodeSvc.Delete(ctx, id, userID); err != nil {
			return classifyError(ctx, err), nil
		}
		return successText(renderDeletedNode(rc, existing, clock.Now().UTC()), ""), nil
	}
}
