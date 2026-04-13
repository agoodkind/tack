package tools

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

func RegisterMyWork(s *mcpserver.MCPServer, reader node.NodeReader, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_my_work",
		Description: "Returns all items assigned to the authenticated user across all workspaces and types. No parameters needed.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{}, Required: []string{}},
	}, myWork(reader, r))
}

func myWork(reader node.NodeReader, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		telemetry.L(ctx).Debug("mcp.my_work")
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		wss, err := r.WorkspacesForUser(ctx, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		var all []*node.NodeListView
		seen := make(map[uuid.UUID]struct{})
		for _, ws := range wss {
			if _, dup := seen[ws.ID]; dup {
				continue
			}
			seen[ws.ID] = struct{}{}
			for _, typeKey := range r.assignableTypeKeys {
				views, err := reader.List(ctx, node.NodeListQuery{
					OrgID: ws.OrgID, WorkspaceID: ws.ID,
					NodeType: typeKey, FilterAssignee: &userID,
				})
				if err != nil {
					continue
				}
				all = append(all, views...)
			}
		}
		return Success(map[string]any{"items": all, "total": len(all)}, ""), nil
	}
}

func RegisterSearch(s *mcpserver.MCPServer, searcher domainsearch.Searcher, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_search",
		Description: "Full-text search across all nodes in a workspace. Returns matching items with facet counts.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string"}, "query": map[string]any{"type": "string"}, "entity_type": map[string]any{"type": "string"}, "project_identifier": map[string]any{"type": "string"}}, Required: []string{"workspace_slug", "query"}},
	}, search(searcher, r))
}

type SearchInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	Query             string  `json:"query"`
	EntityType        *string `json:"entity_type,omitempty"`
	ProjectIdentifier *string `json:"project_identifier,omitempty"`
}

func search(searcher domainsearch.Searcher, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in SearchInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.search",
			slog.String("workspace_slug", in.WorkspaceSlug),
			slog.String("query", in.Query),
		)
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		filters := map[string]string{"workspace_id": ws.ID.String()}
		if in.EntityType != nil && *in.EntityType != "" {
			filters["entity_type"] = *in.EntityType
		}
		if in.ProjectIdentifier != nil && *in.ProjectIdentifier != "" {
			_, proj, projErr := r.Project(ctx, in.WorkspaceSlug, *in.ProjectIdentifier)
			if projErr != nil {
				return ClassifyError(ctx, projErr), nil
			}
			filters["project_id"] = proj.ID.String()
		}
		docs, facets, err := searcher.Search(ctx, "nodes", in.Query, filters)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if docs == nil {
			docs = []domainsearch.NodeDoc{}
		}
		resp := map[string]any{"results": docs, "total": len(docs)}
		if facets != nil {
			resp["facets"] = facets
		}
		return Success(resp, ""), nil
	}
}
