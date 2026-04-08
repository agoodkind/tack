package tools

import (
	"context"
	"encoding/json"

	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/agoodkind/tack/internal/domain/state"
	"github.com/agoodkind/tack/internal/domain/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterWorkspace(
	s *mcp.Server,
	workspaces workspace.Repository,
	projects project.Repository,
	states state.Repository,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_describe_workspace",
		Description: "Introspect a workspace: returns projects, states per project, node types, and property definitions. Call this first to orient yourself before any other operation.",
	}, describeWorkspace(workspaces, projects, states, nodeTypes, properties))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_list_workspaces",
		Description: "List all workspaces the authenticated user has access to",
	}, listWorkspaces(workspaces))
}

// ── describe_workspace ───────────────────────────────────────────────────────

type DescribeWorkspaceInput struct {
	WorkspaceSlug string `json:"workspace_slug" jsonschema:"required,description=Workspace slug"`
}

type DescribeWorkspaceOutput struct {
	Workspace  json.RawMessage   `json:"workspace"`
	Projects   []projectSummary  `json:"projects"`
	NodeTypes  json.RawMessage   `json:"node_types"`
	PropDefs   json.RawMessage   `json:"property_definitions"`
}

type projectSummary struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Identifier string          `json:"identifier"`
	States     json.RawMessage `json:"states"`
}

func describeWorkspace(
	workspaces workspace.Repository,
	projects project.Repository,
	states state.Repository,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
) mcp.ToolHandlerFor[DescribeWorkspaceInput, DescribeWorkspaceOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DescribeWorkspaceInput) (*mcp.CallToolResult, DescribeWorkspaceOutput, error) {
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, DescribeWorkspaceOutput{}, err
		}

		projs, err := projects.List(ctx, ws.ID)
		if err != nil {
			return nil, DescribeWorkspaceOutput{}, err
		}

		summaries := make([]projectSummary, 0, len(projs))
		for _, p := range projs {
			ss, _ := states.List(ctx, p.ID)
			sb, _ := json.Marshal(ss)
			summaries = append(summaries, projectSummary{
				ID:         p.ID.String(),
				Name:       p.Name,
				Identifier: p.Identifier,
				States:     sb,
			})
		}

		nts, _ := nodeTypes.List(ctx, ws.OrgID)
		defs, _ := properties.ListDefs(ctx, ws.OrgID, ws.ID, nil)

		wsb, _ := json.Marshal(ws)
		ntb, _ := json.Marshal(nts)
		db, _ := json.Marshal(defs)

		return nil, DescribeWorkspaceOutput{
			Workspace: wsb,
			Projects:  summaries,
			NodeTypes: ntb,
			PropDefs:  db,
		}, nil
	}
}

// ── list_workspaces ──────────────────────────────────────────────────────────

type ListWorkspacesInput struct{}

type ListWorkspacesOutput struct {
	Workspaces json.RawMessage `json:"workspaces"`
}

func listWorkspaces(workspaces workspace.Repository) mcp.ToolHandlerFor[ListWorkspacesInput, ListWorkspacesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ ListWorkspacesInput) (*mcp.CallToolResult, ListWorkspacesOutput, error) {
		// Without an org filter we can't list all workspaces without auth context.
		// For now return a helpful error directing to describe_workspace.
		return nil, ListWorkspacesOutput{}, errNotImplemented("use tack_describe_workspace with a known workspace_slug")
	}
}
