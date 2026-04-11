package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/issue"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterMyIssues registers the tack_my_issues zero-parameter shortcut tool.
func RegisterMyIssues(s *mcp.Server, issueSvc *service.IssueService, r *Resolver) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_my_issues",
		Description: "Returns all issues assigned to the authenticated user across all projects and workspaces, sorted by updated_at desc. No parameters needed. Use this as a starting point when the user asks what they are working on.",
	}, myIssues(issueSvc, r))
}

type MyIssuesInput struct{}

func myIssues(issueSvc *service.IssueService, r *Resolver) mcp.ToolHandlerFor[MyIssuesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ MyIssuesInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		wss, err := r.WorkspacesForUser(ctx, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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
		return Success(map[string]any{"issues": allIssues, "total": len(allIssues)}, ""), nil, nil
	}
}

func RegisterSearch(s *mcp.Server, searcher domainsearch.Searcher) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_search",
		Description: "Full-text search across all node entities in a workspace. Returns matching items by name and description. Use this when you do not know the exact identifier; use tack_list_* tools for structured filtering.",
	}, search(searcher))
}

type SearchInput struct {
	WorkspaceID string  `json:"workspace_id"`
	Query       string  `json:"query"`
	EntityType  *string `json:"entity_type,omitempty"`
	ProjectID   *string `json:"project_id,omitempty"`
}

func search(searcher domainsearch.Searcher) mcp.ToolHandlerFor[SearchInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		_ = wsID
		filters := map[string]string{
			"workspace_id": in.WorkspaceID,
		}
		if in.EntityType != nil && *in.EntityType != "" {
			filters["entity_type"] = *in.EntityType
		}
		if in.ProjectID != nil && *in.ProjectID != "" {
			filters["project_id"] = *in.ProjectID
		}
		docs, err := searcher.Search(ctx, "nodes", in.Query, filters)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		if docs == nil {
			docs = []domainsearch.NodeDoc{}
		}
		return Success(map[string]any{"items": docs, "total": len(docs)}, ""), nil, nil
	}
}
