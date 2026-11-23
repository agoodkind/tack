package tools

import (
	"context"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

func createHandler(
	nodeType *node.NodeType,
	route scopeRoute,
	binding NodeTypeBinding,
) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}

		entryPointReference, provided := binding.Resolver.entryPointReference(args)
		if !provided {
			return recoverableError(binding.Resolver.entryPointRequiredMessage()), nil
		}

		name, provided := requireString(args, "name")
		if !provided {
			return recoverableError("name is required"), nil
		}

		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}

		workspace, err := binding.Resolver.Workspace(ctx, entryPointReference)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		target, early := resolveCreateTarget(ctx, binding, workspace, route, args)
		if early != nil {
			return early, nil
		}

		rawProps, propsResult := parseToolProps(args["properties"])
		if propsResult != nil {
			return propsResult, nil
		}

		rawProps, parentID, defs, err := normalizeCreateProps(
			ctx,
			binding,
			nodeType,
			workspace.OrgID,
			target.Scope.ID,
			rawProps,
		)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		idempotencyKey, idempotencyFingerprint, idempotencySource, err := createIdempotency(
			ctx,
			req,
			createIdempotencyInput{
				ToolName:       "tack_create_" + strings.ToLower(nodeType.Slug),
				NodeTypeKey:    nodeType.TypeKey,
				EntryPointSlug: entryPointReference,
				Name:           name,
				ParentID:       target.createParentID(parentID),
				ScopeID:        target.Scope.ID,
				UserID:         userID,
				Props:          rawProps,
				Args:           args,
			},
		)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}

		if !isAuthorized(ctx) {
			return permissionDenied(ctx), nil
		}

		result, err := binding.NodeSvc.Create(
			ctx,
			service.CreateInput{
				ParentID:               target.createParentID(parentID),
				ScopeID:                target.Scope.ID,
				NodeTypeKey:            nodeType.TypeKey,
				Name:                   name,
				Props:                  rawProps,
				Relationships:          nil,
				ActorID:                userID,
				IdempotencyKey:         idempotencyKey,
				IdempotencyFingerprint: idempotencyFingerprint,
				IdempotencySource:      idempotencySource,
			},
		)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		return renderCreatedNode(ctx, binding, result, name, rawProps, defs), nil
	}
}
