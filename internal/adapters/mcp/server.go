package mcp

import (
	"net/http"

	"github.com/agoodkind/tack/internal/adapters/mcp/tools"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewHandler returns an http.Handler serving the MCP Streamable HTTP endpoint.
// Services are injected — the MCP adapter calls the service layer directly (in-process).
func NewHandler(issueSvc issue.Service) http.Handler {
	s := mcp.NewServer(&mcp.Implementation{Name: "tack", Version: "0.1.0"}, nil)

	registerIssueTools(s, issueSvc)

	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

func registerIssueTools(s *mcp.Server, svc issue.Service) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_list_work_items",
		Description: "List work items (issues) in a project",
	}, tools.ListWorkItems(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_create_work_item",
		Description: "Create a new work item (issue) in a project",
	}, tools.CreateWorkItem(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_get_work_item",
		Description: "Get a work item by ID",
	}, tools.GetWorkItem(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_update_work_item",
		Description: "Update a work item",
	}, tools.UpdateWorkItem(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_delete_work_item",
		Description: "Soft-delete a work item",
	}, tools.DeleteWorkItem(svc))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_search_work_items",
		Description: "Full-text search across work items in a workspace",
	}, tools.SearchWorkItems(svc))
}
