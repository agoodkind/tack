package tools

import (
	"context"
	"encoding/json"
	"fmt"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

// RegisterReferencePropertyTools registers convenience setters for UUID-valued
// properties that declare a reference target in PropertyDef metadata.
func RegisterReferencePropertyTools(
	s *mcpserver.MCPServer,
	b NodeTypeBinding,
	nodeTypes []*node.NodeType,
	propertyDefs []*node.PropertyDef,
) {
	b.Resolver.setterReferences = make(map[string]string)

	for _, nt := range nodeTypes {
		if nt.Features.Has(node.FeatureExcludeFromGenericTools) {
			continue
		}

		if _, ok := opSet(nt.AllowedOps)[node.OpUpdate]; !ok {
			continue
		}

		for _, def := range propertyDefs {
			if !referencePropertyAppliesToTool(def, nt, b.Resolver.typeIndex) {
				continue
			}

			alias := referencePropertyAlias(def.Name)
			tool := referencePropertyTool(nt, def, alias, b)
			b.Resolver.setterReferences[tool.Name] = typeReferenceParamName(nt.Slug)
			registerTool(s, tool, referencePropertyHandler(nt, def, alias, b))
		}
	}
}

func referencePropertyHandler(
	nt *node.NodeType,
	def *node.PropertyDef,
	alias string,
	b NodeTypeBinding,
) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}

		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}

		parent, err := resolveToolParent(ctx, args, nt, b)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		nodeParam := typeReferenceParamName(nt.Slug)
		nodeRef, ok := requireString(args, nodeParam)
		if !ok {
			return recoverableError(nodeParam + " is required"), nil
		}

		valueRef, ok := requireString(args, alias)
		if !ok {
			return recoverableError(alias + " is required"), nil
		}

		nodeID, err := b.Resolver.resolveNodeUnderParent(ctx, parent, nt, nodeRef)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		existing, err := b.Reader.Get(ctx, nodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		// Stamped before the write, like the generic update handler, so a
		// failed attempt still records which node it targeted. A setter is a
		// mutation on one named node; leaving the carrier empty here would
		// record the change against the zero uuid.
		audit.SetAuditEntity(
			ctx,
			audit.Entity{
				Type:       "node",
				NodeType:   existing.NodeType,
				ID:         existing.ID,
				Identifier: identifierFor(existing, rc),
				Name:       existing.Name,
			},
		)
		props, defs, err := normalizeUpdateProps(
			ctx,
			b,
			nt,
			existing,
			map[string]json.RawMessage{def.Name: mustRawString(valueRef)},
		)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		// Refuse before the write, not only in the response.
		if !isAuthorized(ctx) {
			return permissionDenied(ctx), nil
		}

		view, err := b.NodeSvc.Update(
			ctx,
			service.UpdateInput{
				NodeID:              nodeID,
				Name:                nil,
				Props:               props,
				RelationshipChanges: node.RelationshipChanges{Add: nil, Remove: nil},
				ActorID:             userID,
			},
		)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		changes := describeFieldChanges(
			rc,
			writeChanges{
				Before:        existing,
				After:         view,
				Name:          nil,
				Props:         props,
				ReferenceKeys: referenceKeysOf(defs),
			},
		)

		return successText(renderWriteConfirmation(rc, "Updated", view, changes), ""), nil
	}
}

func resolveToolParent(
	ctx context.Context,
	args argMap,
	nt *node.NodeType,
	b NodeTypeBinding,
) (*node.NodeView, error) {
	entryPointReference, ok := b.Resolver.entryPointReference(args)
	if !ok {
		return nil, fmt.Errorf(
			"%s: %w",
			b.Resolver.entryPointRequiredMessage(),
			domain.ErrInvalidArgument,
		)
	}

	parent, err := b.Resolver.Workspace(ctx, entryPointReference)
	if err != nil {
		return nil, err
	}

	for _, level := range b.Resolver.ScopeChainForType(nt) {
		reference, ok := requireScopeReference(args, level)
		if !ok {
			return nil, fmt.Errorf(
				"%s: %w",
				scopeReferenceRequiredMessage(level),
				domain.ErrInvalidArgument,
			)
		}

		parent, err = b.Resolver.ResolveScope(ctx, parent, level, reference)
		if err != nil {
			return nil, err
		}
	}

	return parent, nil
}
