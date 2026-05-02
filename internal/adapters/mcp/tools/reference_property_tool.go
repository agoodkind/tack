package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
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
			registerTool(s, referencePropertyTool(nt, def, alias, b), referencePropertyHandler(nt, def, alias, b))
		}
	}
}

func referencePropertyAppliesToTool(def *node.PropertyDef, nt *node.NodeType, typeIndex map[string]*node.NodeType) bool {
	if def.Type != node.PropertyTypeUUID {
		return false
	}
	if def.ReferenceTargetTypeKey == "" {
		return false
	}
	if typeIndex[def.ReferenceTargetTypeKey] == nil {
		return false
	}
	return propertyDefAppliesTo(def, nt)
}

func referencePropertyAlias(name string) string {
	return strings.TrimSuffix(name, "_id")
}

func referencePropertyTool(nt *node.NodeType, def *node.PropertyDef, alias string, b NodeTypeBinding) mcpmcp.Tool {
	epParam := b.Resolver.EntryPointParamName()
	chain := b.Resolver.ScopeChainForType(nt)
	nodeParam := strings.ToLower(nt.Slug) + "_identifier"
	fields := []schemaField{
		{Name: epParam, Type: schemaString},
		{Name: nodeParam, Type: schemaString},
		{Name: alias, Type: schemaString, Desc: fmt.Sprintf("UUID or printed reference for %s", alias)},
	}
	required := []string{epParam, nodeParam, alias}
	for _, level := range chain {
		fields = append(fields, schemaField{Name: level.ParamName, Type: schemaString})
		required = append(required, level.ParamName)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_set_%s_%s", strings.ToLower(nt.Slug), alias),
		Description: fmt.Sprintf("Sets %s on a %s using UUID or printed reference input.", def.Name, nt.Name),
		InputSchema: schema{Fields: fields, Required: required}.toMCP(),
	}
}

func referencePropertyHandler(nt *node.NodeType, def *node.PropertyDef, alias string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
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
		nodeParam := strings.ToLower(nt.Slug) + "_identifier"
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
		props, err := normalizeUpdateProps(ctx, b, nt, existing, map[string]json.RawMessage{def.Name: mustRawString(valueRef)})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		view, err := b.NodeSvc.Update(ctx, service.UpdateInput{
			NodeID:  nodeID,
			Props:   props,
			ActorID: userID,
		})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		return successText(renderNode(rc, view), ""), nil
	}
}

func resolveToolParent(ctx context.Context, args argMap, nt *node.NodeType, b NodeTypeBinding) (*node.NodeView, error) {
	epSlug, ok := requireString(args, b.Resolver.EntryPointParamName())
	if !ok {
		return nil, fmt.Errorf("%s is required: %w", b.Resolver.EntryPointParamName(), domain.ErrInvalidArgument)
	}
	parent, err := b.Resolver.Workspace(ctx, epSlug)
	if err != nil {
		return nil, err
	}
	for _, level := range b.Resolver.ScopeChainForType(nt) {
		ident, ok := requireString(args, level.ParamName)
		if !ok {
			return nil, fmt.Errorf("%s is required: %w", level.ParamName, domain.ErrInvalidArgument)
		}
		parent, err = b.Resolver.ResolveScope(ctx, parent, level, ident)
		if err != nil {
			return nil, err
		}
	}
	return parent, nil
}
