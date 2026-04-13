package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

type NodeTypeBinding struct {
	NodeSvc   *service.NodeService
	Reader    node.NodeReader
	Resolver  *Resolver
	TypeIndex map[string]*node.NodeType
}

func RegisterNodeTools(s *mcpserver.MCPServer, nt *node.NodeType, b NodeTypeBinding) {
	if nt.Features.Has(node.FeatureExcludeFromGenericTools) {
		return
	}

	slug := strings.ToLower(nt.Slug)
	plural := strings.ToLower(nt.PluralSlug)
	if plural == "" {
		plural = slug + "s"
	}
	chain := scopeParams(nt, b.Resolver, b.TypeIndex)
	epParam := b.Resolver.EntryPointParamName()
	ops := opSet(nt.AllowedOps)

	if _, ok := ops[node.OpList]; ok {
		s.AddTool(listTool(nt, plural, chain, epParam), listHandler(nt, chain, epParam, b))
	}
	if _, ok := ops[node.OpCreate]; ok {
		s.AddTool(createTool(nt, slug, chain, epParam), createHandler(nt, chain, epParam, b))
	}
	if _, ok := ops[node.OpRead]; ok {
		s.AddTool(getTool(nt, slug), getHandler(nt, b))
	}
	if _, ok := ops[node.OpUpdate]; ok {
		s.AddTool(updateTool(nt, slug), updateHandler(nt, b))
	}
	if _, ok := ops[node.OpDelete]; ok {
		s.AddTool(deleteTool(nt, slug), deleteHandler(b))
	}
}

// scopeParams returns the scope chain for a node type -- the ordered list of
// container identifier parameters needed in the MCP tool schema.
func scopeParams(nt *node.NodeType, r *Resolver, typeIndex map[string]*node.NodeType) []ScopeLevel {
	return r.ScopeChainForType(nt, typeIndex)
}

// ── tool schemas ────────────────────────────────────────────────────────────

func listTool(nt *node.NodeType, plural string, chain []ScopeLevel, epParam string) mcpmcp.Tool {
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
		Description: fmt.Sprintf("Lists all %s.", plural),
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: props, Required: required},
	}
}

func createTool(nt *node.NodeType, slug string, chain []ScopeLevel, epParam string) mcpmcp.Tool {
	props := map[string]any{
		epParam: map[string]any{"type": "string"},
		"name":  map[string]any{"type": "string"},
		"properties": map[string]any{"type": "object", "description": "Property values keyed by name"},
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
		Description: fmt.Sprintf("Gets a %s by ID or identifier (e.g. TACK-65).", nt.Name),
		InputSchema: mcpmcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{"node_id": map[string]any{"type": "string", "description": "UUID or identifier like TACK-65"}},
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
				"node_id":    map[string]any{"type": "string", "description": "UUID or identifier like TACK-65"},
				"name":       map[string]any{"type": "string"},
				"properties": map[string]any{"type": "object", "description": "Property values to update keyed by name"},
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
			Properties: map[string]any{"node_id": map[string]any{"type": "string", "description": "UUID or identifier like TACK-65"}},
			Required:   []string{"node_id"},
		},
	}
}

// ── handlers ────────────────────────────────────────────────────────────────

func listHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		args := req.GetArguments()
		epSlug, _ := args[epParam].(string)
		telemetry.L(ctx).Debug("mcp.list",
			slog.String("type", nt.TypeKey),
			slog.String("entry_point", epSlug),
		)
		ws, err := b.Resolver.Workspace(ctx, epSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		q := node.NodeListQuery{
			OrgID: ws.OrgID, WorkspaceID: ws.ID, NodeType: nt.TypeKey,
		}
		// Walk the scope chain to resolve the deepest container.
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
			q.ByProject = &parent.ID
		}
		views, err := b.Reader.List(ctx, q)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		sort.Slice(views, func(i, j int) bool {
			return views[i].SortOrder < views[j].SortOrder
		})
		plural := nt.PluralSlug
		if plural == "" {
			plural = nt.Slug + "s"
		}
		return Success(map[string]any{plural: views}, ""), nil
	}
}

func createHandler(nt *node.NodeType, chain []ScopeLevel, epParam string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		log := telemetry.L(ctx)
		args := req.GetArguments()
		epSlug, _ := args[epParam].(string)
		name, _ := args["name"].(string)
		var props map[string]any
		if p, ok := args["properties"].(map[string]any); ok {
			props = p
		}
		log.Debug("mcp.create",
			slog.String("type", nt.TypeKey),
			slog.String("name", name),
			slog.String("entry_point", epSlug),
		)
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}

		// Resolve the parent by walking the scope chain.
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

		view, err := b.NodeSvc.Create(ctx, parentID, nt.TypeKey, name, props, nil, nil, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{nt.Slug: view}, ""), nil
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
		telemetry.L(ctx).Debug("mcp.get", slog.String("type", nt.TypeKey), slog.String("node_id", in.NodeID))
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
		telemetry.L(ctx).Debug("mcp.update", slog.String("type", nt.TypeKey), slog.String("node_id", in.NodeID))
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		id, err := b.Resolver.ResolveNodeID(ctx, in.NodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		view, err := b.NodeSvc.Update(ctx, id, in.Name, in.Properties, nil, nil, userID)
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
		telemetry.L(ctx).Debug("mcp.delete", slog.String("node_id", in.NodeID))
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
