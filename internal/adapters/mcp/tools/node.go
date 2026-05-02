package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/service"
)

// NodeTypeBinding carries the dependencies needed by the per-type CRUD tools.
type NodeTypeBinding struct {
	NodeSvc      *service.NodeService
	Reader       node.NodeReader
	PropertyDefs node.PropertyDefRepository
	Resolver     *Resolver
	// Users is optional. When set, response renderers resolve creator and
	// assignee UUIDs to display names. When nil, the renderer falls back to
	// raw IDs.
	Users user.Repository
}

// RegisterNodeTools registers list/create/get/update/delete tools for a NodeType.
func RegisterNodeTools(s *mcpserver.MCPServer, nt *node.NodeType, b NodeTypeBinding) {
	if nt.Features.Has(node.FeatureExcludeFromGenericTools) {
		return
	}
	slug := strings.ToLower(nt.Slug)
	plural := strings.ToLower(nt.PluralSlug)
	if plural == "" {
		plural = slug + "s"
	}
	chain := b.Resolver.ScopeChainForType(nt)
	epParam := b.Resolver.EntryPointParamName()
	ops := opSet(nt.AllowedOps)

	if _, ok := ops[node.OpList]; ok {
		registerTool(s, listTool(plural, chain, epParam), listHandler(nt, chain, epParam, b))
	}
	if _, ok := ops[node.OpCreate]; ok {
		registerTool(s, createTool(nt, slug, chain, epParam), createHandler(nt, chain, epParam, b))
	}
	if _, ok := ops[node.OpRead]; ok {
		registerTool(s, getTool(nt, slug), getHandler(nt, b))
	}
	if _, ok := ops[node.OpUpdate]; ok {
		registerTool(s, updateTool(nt, slug), updateHandler(nt, b))
	}
	if _, ok := ops[node.OpDelete]; ok {
		registerTool(s, deleteTool(nt, slug), deleteHandler(nt, b))
	}
}

func listTool(plural string, chain []ScopeLevel, epParam string) mcpmcp.Tool {
	fields := []schemaField{{Name: epParam, Type: schemaString}}
	required := []string{epParam}
	for _, level := range chain {
		fields = append(fields, schemaField{Name: level.ParamName, Type: schemaString})
		required = append(required, level.ParamName)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_list_%s", plural),
		Description: fmt.Sprintf("Lists %s under the given scope.", plural),
		InputSchema: schema{Fields: fields, Required: required}.toMCP(),
	}
}

func createTool(nt *node.NodeType, slug string, chain []ScopeLevel, epParam string) mcpmcp.Tool {
	fields := []schemaField{
		{Name: epParam, Type: schemaString},
		{Name: "name", Type: schemaString},
		{Name: "properties", Type: schemaObject, Desc: "Property values keyed by name"},
	}
	required := []string{epParam, "name"}
	for _, level := range chain {
		fields = append(fields, schemaField{Name: level.ParamName, Type: schemaString})
		required = append(required, level.ParamName)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_create_%s", slug),
		Description: fmt.Sprintf("Creates a %s.", nt.Name),
		InputSchema: schema{Fields: fields, Required: required}.toMCP(),
	}
}

func getTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	description := fmt.Sprintf("Gets a %s by UUID.", nt.Name)
	switch nt.Reference.Strategy {
	case node.ReferenceScopedSequence:
		description = fmt.Sprintf("Gets a %s by UUID or identifier like TACK-65.", nt.Name)
	case node.ReferenceScopedProperty:
		description = fmt.Sprintf("Gets a %s by UUID or scoped reference like PROJECT::Name.", nt.Name)
	case node.ReferenceDirectSlug:
		description = fmt.Sprintf("Gets a %s by UUID or identifier.", nt.Name)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_get_%s", slug),
		Description: description,
		InputSchema: schema{
			Fields:   []schemaField{{Name: "node_id", Type: schemaString}},
			Required: []string{"node_id"},
		}.toMCP(),
	}
}

func updateTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_update_%s", slug),
		Description: fmt.Sprintf("Updates a %s. Only provided fields change.", nt.Name),
		InputSchema: schema{
			Fields: []schemaField{
				{Name: "node_id", Type: schemaString},
				{Name: "name", Type: schemaString},
				{Name: "properties", Type: schemaObject},
			},
			Required: []string{"node_id"},
		}.toMCP(),
	}
}

func deleteTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_delete_%s", slug),
		Description: fmt.Sprintf("Deletes a %s.", nt.Name),
		InputSchema: schema{
			Fields:   []schemaField{{Name: "node_id", Type: schemaString}},
			Required: []string{"node_id"},
		}.toMCP(),
	}
}

func listHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}
		epSlug, ok := requireString(args, epParam)
		if !ok {
			return recoverableError(epParam + " is required"), nil
		}
		ws, err := b.Resolver.Workspace(ctx, epSlug)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		q := node.NodeListQuery{OrgID: ws.OrgID, NodeType: nt.TypeKey}

		// Walk the scope chain to the deepest container, then narrow via an
		// indexed property instead of a type-wide view scan.
		var parentID uuid.UUID
		if len(chain) > 0 {
			parent := ws
			for _, level := range chain {
				ident, ok := requireString(args, level.ParamName)
				if !ok {
					return recoverableError(level.ParamName + " is required"), nil
				}
				parent, err = b.Resolver.ResolveScope(ctx, parent, level, ident)
				if err != nil {
					return classifyError(ctx, err), nil
				}
			}
			parentID = parent.ID
		} else {
			parentID = ws.ID
		}
		parentIDRaw, _ := json.Marshal(parentID.String())
		q.ByProperty = &node.PropertyMatch{
			PropName: listScopeProperty(nt, chain),
			Value:    parentIDRaw,
		}

		views, err := b.Reader.List(ctx, q)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		return successText(renderList(rc, plural, views), ""), nil
	}
}

func createHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}
		epSlug, ok := requireString(args, epParam)
		if !ok {
			return recoverableError(epParam + " is required"), nil
		}
		name, ok := requireString(args, "name")
		if !ok {
			return recoverableError("name is required"), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}
		ws, err := b.Resolver.Workspace(ctx, epSlug)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		scopeID := ws.ID
		if len(chain) > 0 {
			parent := ws
			for _, level := range chain {
				ident, ok := requireString(args, level.ParamName)
				if !ok {
					return recoverableError(level.ParamName + " is required"), nil
				}
				parent, err = b.Resolver.ResolveScope(ctx, parent, level, ident)
				if err != nil {
					return classifyError(ctx, err), nil
				}
			}
			scopeID = parent.ID
		}

		// Convert property values to raw JSON. Tolerates payloads sent as a
		// stringified JSON blob; see TACK-161.
		rawProps, err := parseProps(args["properties"])
		if err != nil {
			return recoverableError("invalid properties payload: " + err.Error()), nil
		}

		rawProps, parentID, err := normalizeCreateProps(ctx, b, nt, ws.OrgID, scopeID, rawProps)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		toolName := fmt.Sprintf("tack_create_%s", strings.ToLower(nt.Slug))
		idempotencyKey, idempotencyFingerprint, idempotencySource, err := createIdempotency(ctx, req, createIdempotencyInput{
			ToolName:       toolName,
			NodeTypeKey:    nt.TypeKey,
			EntryPointSlug: epSlug,
			Name:           name,
			ParentID:       parentID,
			ScopeID:        scopeID,
			UserID:         userID,
			Props:          rawProps,
			Args:           args,
		})
		if err != nil {
			return unexpectedError(ctx, err), nil
		}
		result, err := b.NodeSvc.Create(ctx, service.CreateInput{
			ParentID:               parentID,
			ScopeID:                scopeID,
			NodeTypeKey:            nt.TypeKey,
			Name:                   name,
			Props:                  rawProps,
			ActorID:                userID,
			IdempotencyKey:         idempotencyKey,
			IdempotencyFingerprint: idempotencyFingerprint,
			IdempotencySource:      idempotencySource,
		})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		instr := ""
		if result.Existed {
			instr = "This create matched an existing operation. No new write was performed."
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
		return successText(renderNode(rc, result.View), instr), nil
	}
}

type getInput struct {
	NodeID string `json:"node_id"`
}

func getHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in getInput
		if err := req.BindArguments(&in); err != nil {
			return recoverableError(err.Error()), nil
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
		id, err := b.Resolver.ResolveTypedNodeID(ctx, nt, in.NodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		args, err := bindArgs(req)
		if err != nil {
			return recoverableError(err.Error()), nil
		}
		rawProps, err := parseProps(args["properties"])
		if err != nil {
			return recoverableError("invalid properties payload: " + err.Error()), nil
		}
		existing, err := b.Reader.Get(ctx, id)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		rawProps, err = normalizeUpdateProps(ctx, b, nt, existing, rawProps)
		if err != nil {
			return classifyError(ctx, err), nil
		}

		view, err := b.NodeSvc.Update(ctx, service.UpdateInput{
			NodeID:  id,
			Name:    in.Name,
			Props:   rawProps,
			ActorID: userID,
		})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		rc := newRenderCtxWithTypes(ctx, b.Reader, b.Users, b.Resolver.typeIndex)
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
		id, err := b.Resolver.ResolveTypedNodeID(ctx, nt, in.NodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		if err := b.NodeSvc.Delete(ctx, id, userID); err != nil {
			return classifyError(ctx, err), nil
		}
		return successText("deleted "+in.NodeID, ""), nil
	}
}

func opSet(ops []node.Op) map[node.Op]struct{} {
	m := make(map[node.Op]struct{}, len(ops))
	for _, op := range ops {
		m[op] = struct{}{}
	}
	return m
}
