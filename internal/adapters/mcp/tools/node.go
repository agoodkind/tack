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
	props := map[string]any{
		epParam: map[string]any{"type": "string"},
	}
	required := []string{epParam}
	for _, level := range chain {
		props[level.ParamName] = map[string]any{"type": "string"}
		required = append(required, level.ParamName)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_list_%s", plural),
		Description: fmt.Sprintf("Lists %s under the given scope.", plural),
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: props, Required: required},
	}
}

func createTool(nt *node.NodeType, slug string, chain []ScopeLevel, epParam string) mcpmcp.Tool {
	props := map[string]any{
		epParam:      map[string]any{"type": "string"},
		"name":       map[string]any{"type": "string"},
		"properties": map[string]any{"type": "object", "description": "Property values keyed by name"},
		"idempotency_key": map[string]any{
			"type":        "string",
			"description": "Optional. When provided, a retry with the same key returns the previously created node instead of creating a duplicate.",
		},
	}
	required := []string{epParam, "name"}
	for _, level := range chain {
		props[level.ParamName] = map[string]any{"type": "string"}
		required = append(required, level.ParamName)
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_create_%s", slug),
		Description: fmt.Sprintf("Creates a %s.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: props, Required: required},
	}
}

func getTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_get_%s", slug),
		Description: fmt.Sprintf("Gets a %s by ID or identifier like TACK-65.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{"node_id": map[string]any{"type": "string"}},
			Required:   []string{"node_id"},
		},
	}
}

func updateTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_update_%s", slug),
		Description: fmt.Sprintf("Updates a %s. Only provided fields change.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"node_id":    map[string]any{"type": "string"},
				"name":       map[string]any{"type": "string"},
				"properties": map[string]any{"type": "object"},
			},
			Required: []string{"node_id"},
		},
	}
}

func deleteTool(nt *node.NodeType, slug string) mcpmcp.Tool {
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_delete_%s", slug),
		Description: fmt.Sprintf("Deletes a %s.", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{"node_id": map[string]any{"type": "string"}},
			Required:   []string{"node_id"},
		},
	}
}

func listHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args := req.GetArguments()
		epSlug, _ := args[epParam].(string)
		ws, err := b.Resolver.Workspace(ctx, epSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		q := node.NodeListQuery{OrgID: ws.OrgID, NodeType: nt.TypeKey}

		// Walk the scope chain to find the deepest container; restrict via parent_id prop.
		if len(chain) > 0 {
			parent := ws
			for _, level := range chain {
				ident, _ := args[level.ParamName].(string)
				if ident == "" {
					return RecoverableError(level.ParamName + " is required"), nil
				}
				parent, err = b.Resolver.ResolveScope(ctx, parent, level, ident)
				if err != nil {
					return ClassifyError(ctx, err), nil
				}
			}
			parentIDRaw, _ := json.Marshal(parent.ID.String())
			q.PropFilters = append(q.PropFilters, node.PropertyMatch{
				PropName: "parent_id",
				Value:    parentIDRaw,
			})
		} else {
			parentIDRaw, _ := json.Marshal(ws.ID.String())
			q.PropFilters = append(q.PropFilters, node.PropertyMatch{
				PropName: "parent_id",
				Value:    parentIDRaw,
			})
		}

		views, err := b.Reader.List(ctx, q)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}
		return Success(map[string]any{plural: views}, ""), nil
	}
}

func createHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args := req.GetArguments()
		epSlug, _ := args[epParam].(string)
		name, _ := args["name"].(string)
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, err := b.Resolver.Workspace(ctx, epSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		parentID := ws.ID
		if len(chain) > 0 {
			parent := ws
			for _, level := range chain {
				ident, _ := args[level.ParamName].(string)
				if ident == "" {
					return RecoverableError(level.ParamName + " is required"), nil
				}
				parent, err = b.Resolver.ResolveScope(ctx, parent, level, ident)
				if err != nil {
					return ClassifyError(ctx, err), nil
				}
			}
			parentID = parent.ID
		}

		// Convert property values to raw JSON.
		rawProps := make(map[string]json.RawMessage)
		if p, ok := args["properties"].(map[string]any); ok {
			for k, v := range p {
				raw, err := json.Marshal(v)
				if err != nil {
					continue
				}
				rawProps[k] = raw
			}
		}

		idempotencyKey, _ := args["idempotency_key"].(string)
		result, err := b.NodeSvc.Create(ctx, service.CreateInput{
			ParentID:       parentID,
			NodeTypeKey:    nt.TypeKey,
			Name:           name,
			Props:          rawProps,
			ActorID:        userID,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		payload := map[string]any{nt.Slug: result.View}
		if result.Existed {
			payload["already_existed"] = true
		}
		instr := ""
		if result.Existed {
			instr = "Idempotency key matched; returning the existing node. No new write was performed."
		}
		return Success(payload, instr), nil
	}
}

type getInput struct {
	NodeID string `json:"node_id"`
}

func getHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in getInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		id, err := b.Resolver.ResolveNodeID(ctx, in.NodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		view, err := b.Reader.Get(ctx, id)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if view == nil {
			return ClassifyError(ctx, domain.ErrNotFound), nil
		}
		return Success(map[string]any{nt.Slug: view}, ""), nil
	}
}

type updateInput struct {
	NodeID     string         `json:"node_id"`
	Name       *string        `json:"name,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

func updateHandler(nt *node.NodeType, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in updateInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		id, err := b.Resolver.ResolveNodeID(ctx, in.NodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}

		rawProps := make(map[string]json.RawMessage)
		for k, v := range in.Properties {
			raw, err := json.Marshal(v)
			if err != nil {
				continue
			}
			rawProps[k] = raw
		}

		view, err := b.NodeSvc.Update(ctx, service.UpdateInput{
			NodeID:  id,
			Name:    in.Name,
			Props:   rawProps,
			ActorID: userID,
		})
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{nt.Slug: view}, ""), nil
	}
}

type deleteInput struct {
	NodeID string `json:"node_id"`
}

func deleteHandler(b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in deleteInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		id, err := b.Resolver.ResolveNodeID(ctx, in.NodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if err := b.NodeSvc.Delete(ctx, id, userID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"ok": true}, ""), nil
	}
}

func opSet(ops []node.Op) map[node.Op]struct{} {
	m := make(map[node.Op]struct{}, len(ops))
	for _, op := range ops {
		m[op] = struct{}{}
	}
	return m
}
