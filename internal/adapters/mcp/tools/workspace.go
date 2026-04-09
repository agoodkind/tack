package tools

import (
	"context"

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
	WorkspaceSlug string `json:"workspace_slug"`
}

type DescribeWorkspaceOutput struct {
	Workspace  any   `json:"workspace"`
	Projects   []projectSummary  `json:"projects"`
	NodeTypes  any   `json:"node_types"`
	PropDefs   any   `json:"property_definitions"`
}

type projectSummary struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Identifier string          `json:"identifier"`
	States     any `json:"states"`
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
			summaries = append(summaries, projectSummary{
				ID:         p.ID.String(),
				Name:       p.Name,
				Identifier: p.Identifier,
				States:     ss,
			})
		}

		nts, _ := nodeTypes.List(ctx, ws.OrgID)
		defs, _ := properties.ListDefs(ctx, ws.OrgID, ws.ID, nil)

		return nil, DescribeWorkspaceOutput{
			Workspace: ws,
			Projects:  summaries,
			NodeTypes: nts,
			PropDefs:  defs,
		}, nil
	}
}

// ── list_workspaces ──────────────────────────────────────────────────────────

type ListWorkspacesInput struct{}

type ListWorkspacesOutput struct {
	Workspaces any `json:"workspaces"`
}

func listWorkspaces(workspaces workspace.Repository) mcp.ToolHandlerFor[ListWorkspacesInput, ListWorkspacesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ ListWorkspacesInput) (*mcp.CallToolResult, ListWorkspacesOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, ListWorkspacesOutput{}, err
		}
		ws, err := workspaces.ListForUser(ctx, userID)
		if err != nil {
			return nil, ListWorkspacesOutput{}, err
		}
		return nil, ListWorkspacesOutput{Workspaces: ws}, nil
	}
}
