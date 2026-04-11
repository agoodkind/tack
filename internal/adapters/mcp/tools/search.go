package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/issue"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

// RegisterMyIssues registers the tack_my_issues zero-parameter shortcut tool.
func RegisterMyIssues(s *mcpserver.MCPServer, issueSvc *service.IssueService, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_my_issues",
		Description: "Returns all issues assigned to the authenticated user across all projects and workspaces, sorted by updated_at desc. No parameters needed. Use this as a starting point when the user asks what they are working on.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{}, Required: []string{}},
	}, myIssues(issueSvc, r))
}

type MyIssuesInput struct{}

func myIssues(issueSvc *service.IssueService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		wss, err := r.WorkspacesForUser(ctx, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		var allIssues []*issue.Issue
		seen := make(map[uuid.UUID]struct{})
		for _, ws := range wss {
			if _, dup := seen[ws.ID]; dup {
				continue
			}
			seen[ws.ID] = struct{}{}
			filter := issue.ListFilter{
				WorkspaceID: ws.ID,
				AssigneeIDs: []uuid.UUID{userID},
			}
			items, _, issErr := issueSvc.List(ctx, filter)
			if issErr != nil {
				continue
			}
			allIssues = append(allIssues, items...)
		}
		return Success(map[string]any{"issues": allIssues, "total": len(allIssues)}, ""), nil
	}
}

func RegisterSearch(s *mcpserver.MCPServer, searcher domainsearch.Searcher, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_search",
		Description: "Full-text search across all node entities in a workspace. Returns matching items with facet counts by entity_type and project. Use this when you do not know the exact identifier or type; use tack_list_* tools for structured filtering within a known type.",
		InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string", "description": "The workspace slug"}, "query": map[string]any{"type": "string", "description": "The search query"}, "entity_type": map[string]any{"type": "string", "description": "The entity type"}, "project_identifier": map[string]any{"type": "string", "description": "The project identifier"}}, Required: []string{"workspace_slug", "query"}},
	}, search(searcher, r))
}

type SearchInput struct {
	WorkspaceSlug      string  `json:"workspace_slug"`
	Query              string  `json:"query"`
	EntityType         *string `json:"entity_type,omitempty"`
	ProjectIdentifier  *string `json:"project_identifier,omitempty"`
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
		filters := map[string]string{
			"workspace_id": ws.ID.String(),
		}
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
		resp := map[string]any{
			"results": docs,
			"total":   len(docs),
		}
		if facets != nil {
			resp["facets"] = facets
		}
		instruction := ""
		if len(docs) == 0 {
			instruction = "No results found. Try broadening the query or removing filters."
		}
		return Success(resp, instruction), nil
	}
}
