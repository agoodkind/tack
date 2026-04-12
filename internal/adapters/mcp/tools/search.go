package tools

import (
	"context"

	"github.com/google/uuid"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/domain/node"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func RegisterMyIssues(s *mcpserver.MCPServer, reader node.NodeReader, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_my_issues",
		Description: "Returns all issues assigned to the authenticated user across all workspaces. No parameters needed.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{}, Required: []string{}},
	}, myIssues(reader, r))
}

func myIssues(reader node.NodeReader, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
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
			views, err := reader.List(ctx, node.NodeListQuery{
				OrgID: ws.OrgID, WorkspaceID: ws.ID,
				NodeType: node.NodeTypeIssue, FilterAssignee: &userID,
			})
			if err != nil {
				continue
			}
			all = append(all, views...)
		}
		return Success(map[string]any{"issues": all, "total": len(all)}, ""), nil
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
