package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/project"
	"goodkind.io/tack/internal/domain/state"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/domain/workspace"
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

func describeWorkspace(
	workspaces workspace.Repository,
	projects project.Repository,
	states state.Repository,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
) mcp.ToolHandlerFor[DescribeWorkspaceInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DescribeWorkspaceInput) (*mcp.CallToolResult, any, error) {
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, nil, err
		}

		projs, err := projects.List(ctx, ws.ID)
		if err != nil {
			return nil, nil, err
		}

		type projectSummary struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Identifier string `json:"identifier"`
			States     any    `json:"states"`
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

		return nil, map[string]any{
			"workspace":             ws,
			"projects":              summaries,
			"node_types":            nts,
			"property_definitions":  defs,
		}, nil
	}
}

// RegisterMembers registers the tack_list_members tool.
func RegisterMembers(s *mcp.Server, workspaces workspace.Repository, orgs org.Repository, users user.Repository) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_list_members",
		Description: "List all members of a workspace with their display name and email. Returns member user ID, display name, email, and role. Use this to look up user emails before assigning issues.",
	}, listMembers(workspaces, orgs, users))
}

// ── list_members ─────────────────────────────────────────────────────────────

type ListMembersInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
}

func listMembers(workspaces workspace.Repository, orgs org.Repository, users user.Repository) mcp.ToolHandlerFor[ListMembersInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListMembersInput) (*mcp.CallToolResult, any, error) {
		ws, err := workspaces.GetBySlug(ctx, in.WorkspaceSlug)
		if err != nil {
			return nil, nil, err
		}
		members, err := orgs.ListMembers(ctx, ws.OrgID)
		if err != nil {
			return nil, nil, err
		}
		type memberView struct {
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
			Role        int    `json:"role"`
		}
		views := make([]memberView, 0, len(members))
		for _, m := range members {
			u, err := users.GetByID(ctx, m.UserID)
			if err != nil {
				continue
			}
			views = append(views, memberView{
				UserID:      m.UserID.String(),
				DisplayName: u.DisplayName,
				Email:       u.Email,
				Role:        m.Role,
			})
		}
		return nil, map[string]any{"members": views}, nil
	}
}

// ── list_workspaces ──────────────────────────────────────────────────────────

type ListWorkspacesInput struct{}

func listWorkspaces(workspaces workspace.Repository) mcp.ToolHandlerFor[ListWorkspacesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ ListWorkspacesInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		ws, err := workspaces.ListForUser(ctx, userID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"workspaces": ws}, nil
	}
}
