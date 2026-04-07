package mcp

import (
	"github.com/agoodkind/tack/internal/adapters/mcp/tools"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// New returns a configured MCP server with all tools registered.
// Services are injected — the MCP adapter calls the service layer directly
// (in-process). It is a peer adapter alongside REST, not a special internal hook.
func New(issueSvc issue.Service) *server.MCPServer {
	s := server.NewMCPServer(
		"go-pm",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	registerIssueTools(s, issueSvc)

	return s
}

func registerIssueTools(s *server.MCPServer, svc issue.Service) {
	s.AddTool(mcp.NewTool(
		"go_pm_list_work_items",
		mcp.WithDescription("List work items (issues) in a project"),
		mcp.WithString("workspace_slug", mcp.Required(), mcp.Description("Workspace slug")),
		mcp.WithString("project_id", mcp.Required(), mcp.Description("Project UUID")),
		mcp.WithString("state_id", mcp.Description("Filter by state UUID")),
		mcp.WithString("priority", mcp.Description("Filter by priority: none|low|medium|high|urgent")),
		mcp.WithString("assignee_id", mcp.Description("Filter by assignee UUID")),
	), tools.ListWorkItems(svc))

	s.AddTool(mcp.NewTool(
		"go_pm_create_work_item",
		mcp.WithDescription("Create a new work item (issue) in a project"),
		mcp.WithString("workspace_slug", mcp.Required(), mcp.Description("Workspace slug")),
		mcp.WithString("project_id", mcp.Required(), mcp.Description("Project UUID")),
		mcp.WithString("name", mcp.Required(), mcp.Description("Issue title")),
		mcp.WithString("description", mcp.Description("Issue description (HTML)")),
		mcp.WithString("priority", mcp.Description("none|low|medium|high|urgent")),
		mcp.WithString("state_id", mcp.Description("State UUID")),
		mcp.WithString("assignee_id", mcp.Description("Assignee user UUID")),
	), tools.CreateWorkItem(svc))

	s.AddTool(mcp.NewTool(
		"go_pm_get_work_item",
		mcp.WithDescription("Get a work item by ID"),
		mcp.WithString("workspace_slug", mcp.Required(), mcp.Description("Workspace slug")),
		mcp.WithString("project_id", mcp.Required(), mcp.Description("Project UUID")),
		mcp.WithString("issue_id", mcp.Required(), mcp.Description("Issue UUID")),
	), tools.GetWorkItem(svc))

	s.AddTool(mcp.NewTool(
		"go_pm_update_work_item",
		mcp.WithDescription("Update a work item"),
		mcp.WithString("workspace_slug", mcp.Required(), mcp.Description("Workspace slug")),
		mcp.WithString("project_id", mcp.Required(), mcp.Description("Project UUID")),
		mcp.WithString("issue_id", mcp.Required(), mcp.Description("Issue UUID")),
		mcp.WithString("name", mcp.Description("New title")),
		mcp.WithString("priority", mcp.Description("none|low|medium|high|urgent")),
		mcp.WithString("state_id", mcp.Description("New state UUID")),
	), tools.UpdateWorkItem(svc))

	s.AddTool(mcp.NewTool(
		"go_pm_delete_work_item",
		mcp.WithDescription("Delete (soft) a work item"),
		mcp.WithString("workspace_slug", mcp.Required(), mcp.Description("Workspace slug")),
		mcp.WithString("project_id", mcp.Required(), mcp.Description("Project UUID")),
		mcp.WithString("issue_id", mcp.Required(), mcp.Description("Issue UUID")),
	), tools.DeleteWorkItem(svc))

	s.AddTool(mcp.NewTool(
		"go_pm_search_work_items",
		mcp.WithDescription("Full-text search across work items in a workspace"),
		mcp.WithString("workspace_slug", mcp.Required(), mcp.Description("Workspace slug")),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query (websearch syntax)")),
		mcp.WithString("project_id", mcp.Description("Scope to a specific project")),
	), tools.SearchWorkItems(svc))
}
