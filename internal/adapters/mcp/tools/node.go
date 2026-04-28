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
	"goodkind.io/tack/internal/service"
)

// NodeTypeBinding carries the dependencies needed by the per-type CRUD tools.
type NodeTypeBinding struct {
	NodeSvc  *service.NodeService
	Reader   node.NodeReader
	Resolver *Resolver
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
		registerTool(s, deleteTool(nt, slug), deleteHandler(b))
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
		{Name: "idempotency_key", Type: schemaString, Desc: "Optional. When provided, a retry with the same key returns the previously created node instead of creating a duplicate."},
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
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_get_%s", slug),
		Description: fmt.Sprintf("Gets a %s by ID or identifier like TACK-65.", nt.Name),
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

		// Walk the scope chain to the deepest container, then narrow via the
		// indexed parent_id secondary index instead of a type-wide view scan.
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
			PropName: "parent_id",
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
		return successWrapped(plural, views, nil, ""), nil
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

		// Honor properties.parent_id when set to a deeper container under the
		// same org as the chain-resolved scope. Sequence numbers stay scope-
		// scoped (see service.CreateInput.ScopeID). See TACK-164.
		parentID := scopeID
		if rawProps != nil {
			if raw, ok := rawProps["parent_id"]; ok && len(raw) > 0 {
				var s string
				if err := json.Unmarshal(raw, &s); err == nil && s != "" {
					if pid, err := uuid.Parse(s); err == nil && pid != uuid.Nil {
						parentResolve, err := b.Reader.Resolve(ctx, pid)
						if err != nil || parentResolve == nil {
							return recoverableError("properties.parent_id does not resolve to a known node: " + s), nil
						}
						if parentResolve.OrgID != ws.OrgID {
							return recoverableError("properties.parent_id is in a different org than the workspace"), nil
						}
						parentID = pid
					}
				}
			}
			// Service.Create overwrites Props["parent_id"] from in.ParentID,
			// so we drop the caller-supplied entry to avoid two sources of
			// truth diverging if the input was ill-formed.
			delete(rawProps, "parent_id")
		}

		idempotencyKey := optionalString(args, "idempotency_key")
		result, err := b.NodeSvc.Create(ctx, service.CreateInput{
			ParentID:       parentID,
			ScopeID:        scopeID,
			NodeTypeKey:    nt.TypeKey,
			Name:           name,
			Props:          rawProps,
			ActorID:        userID,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		var extras map[string]json.RawMessage
		instr := ""
		if result.Existed {
			extras = map[string]json.RawMessage{"already_existed": json.RawMessage(`true`)}
			instr = "Idempotency key matched; returning the existing node. No new write was performed."
		}
		return successWrapped(nt.Slug, result.View, extras, instr), nil
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
		id, err := b.Resolver.ResolveNodeID(ctx, in.NodeID)
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
		return successWrapped(nt.Slug, view, nil, ""), nil
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
		id, err := b.Resolver.ResolveNodeID(ctx, in.NodeID)
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

		view, err := b.NodeSvc.Update(ctx, service.UpdateInput{
			NodeID:  id,
			Name:    in.Name,
			Props:   rawProps,
			ActorID: userID,
		})
		if err != nil {
			return classifyError(ctx, err), nil
		}
		return successWrapped(nt.Slug, view, nil, ""), nil
	}
}

type deleteInput struct {
	NodeID string `json:"node_id"`
}

func deleteHandler(b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in deleteInput
		if err := req.BindArguments(&in); err != nil {
			return recoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return unexpectedError(ctx, err), nil
		}
		id, err := b.Resolver.ResolveNodeID(ctx, in.NodeID)
		if err != nil {
			return classifyError(ctx, err), nil
		}
		if err := b.NodeSvc.Delete(ctx, id, userID); err != nil {
			return classifyError(ctx, err), nil
		}
		return successJSON(okResp{OK: true}, ""), nil
	}
}

func opSet(ops []node.Op) map[node.Op]struct{} {
	m := make(map[node.Op]struct{}, len(ops))
	for _, op := range ops {
		m[op] = struct{}{}
	}
	return m
}
