package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/service"
)

type NodeTypeBinding struct {
	NodeSvc  *service.NodeService
	Reader   node.NodeReader
	Resolver *Resolver
}

func RegisterNodeTools(s *mcpserver.MCPServer, nt *node.NodeType, b NodeTypeBinding) {
	// Org and workspace have dedicated registrations (describe_workspace, list_workspaces)
	switch nt.TypeKey {
	case node.NodeTypeOrg, node.NodeTypeWorkspace:
		return
	}

	slug := strings.ToLower(nt.Slug)
	plural := strings.ToLower(nt.PluralSlug)
	if plural == "" {
		plural = slug + "s"
	}
	scope := nodeParentScope(nt)
	ops := opSet(nt.AllowedOps)

	if _, ok := ops[node.OpList]; ok {
		s.AddTool(listTool(nt, plural, scope), listHandler(nt, scope, b))
	}
	if _, ok := ops[node.OpCreate]; ok {
		s.AddTool(createTool(nt, slug, scope), createHandler(nt, scope, b))
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

func nodeParentScope(nt *node.NodeType) string {
	for _, parent := range nt.CanLiveUnder {
		if parent == node.NodeTypeProject {
			return "project"
		}
	}
	return "workspace"
}

// ── tool schemas ────────────────────────────────────────────────────────────

func listTool(nt *node.NodeType, plural, scope string) mcpmcp.Tool {
	props := map[string]any{
		"workspace_slug": map[string]any{"type": "string"},
	}
	required := []string{"workspace_slug"}
	if scope == "project" {
		props["project_identifier"] = map[string]any{"type": "string"}
		required = append(required, "project_identifier")
	}
	return mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_list_%s", plural),
		Description: fmt.Sprintf("Lists all %s.", plural),
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: props, Required: required},
	}
}

func createTool(nt *node.NodeType, slug, scope string) mcpmcp.Tool {
	props := map[string]any{
		"workspace_slug": map[string]any{"type": "string"},
		"name":           map[string]any{"type": "string"},
		"properties":     map[string]any{"type": "object", "description": "Property values keyed by name"},
	}
	required := []string{"workspace_slug", "name"}
	if scope == "project" {
		props["project_identifier"] = map[string]any{"type": "string"}
		required = append(required, "project_identifier")
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
		Description: fmt.Sprintf("Gets a %s by ID.", nt.Name),
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
			Properties: map[string]any{"node_id": map[string]any{"type": "string"}},
			Required:   []string{"node_id"},
		},
	}
}

// ── handlers ────────────────────────────────────────────────────────────────

type listInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier *string `json:"project_identifier,omitempty"`
}

func listHandler(nt *node.NodeType, scope string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in listInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := b.Resolver.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		q := node.NodeListQuery{
			OrgID: ws.OrgID, WorkspaceID: ws.ID, NodeType: nt.TypeKey,
		}
		if scope == "project" {
			if in.ProjectIdentifier == nil || *in.ProjectIdentifier == "" {
				return RecoverableError("project_identifier is required"), nil
			}
			_, proj, err := b.Resolver.Project(ctx, in.WorkspaceSlug, *in.ProjectIdentifier)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			q.ByProject = &proj.ID
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

type createInput struct {
	WorkspaceSlug     string         `json:"workspace_slug"`
	ProjectIdentifier *string        `json:"project_identifier,omitempty"`
	Name              string         `json:"name"`
	Properties        map[string]any `json:"properties,omitempty"`
}

func createHandler(nt *node.NodeType, scope string, b NodeTypeBinding) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in createInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}

		// Resolve parent: workspace or project
		var parentID uuid.UUID
		if scope == "project" {
			if in.ProjectIdentifier == nil || *in.ProjectIdentifier == "" {
				return RecoverableError("project_identifier is required"), nil
			}
			_, proj, err := b.Resolver.Project(ctx, in.WorkspaceSlug, *in.ProjectIdentifier)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			parentID = proj.ID
		} else {
			ws, err := b.Resolver.Workspace(ctx, in.WorkspaceSlug)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			parentID = ws.ID
		}

		view, err := b.NodeSvc.Create(ctx, parentID, nt.TypeKey, in.Name, in.Properties, nil, nil, userID)
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
		id, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
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
		id, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
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
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		id, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
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
