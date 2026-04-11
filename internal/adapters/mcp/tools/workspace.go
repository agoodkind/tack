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
		Description: "Introspects a workspace: returns projects with states, node types, and property definitions. Call this first before any other workspace-scoped operation.",
	}, describeWorkspace(workspaces, projects, states, nodeTypes, properties))

	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_list_workspaces",
		Description: "Lists all workspaces the authenticated user has access to. Returns workspace slug, name, and ID. Use the slug in all workspace_slug parameters.",
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
			return ClassifyError(ctx, err), nil, nil
		}

		projs, err := projects.List(ctx, ws.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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

		return Success(map[string]any{
			"workspace":             ws,
			"projects":              summaries,
			"node_types":            nts,
			"property_definitions":  defs,
		}, "Use the project identifiers here as project_identifier in tack_list_issues and related tools."), nil, nil
	}
}

// RegisterMembers registers the tack_list_members tool.
func RegisterMembers(s *mcp.Server, workspaces workspace.Repository, orgs org.Repository, users user.Repository) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "tack_list_members",
		Description: "Lists all members of a workspace with display name and email. Returns user_id, display_name, email, and role. Use this to look up user IDs before assigning issues.",
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
			return ClassifyError(ctx, err), nil, nil
		}
		members, err := orgs.ListMembers(ctx, ws.OrgID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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
		return Success(map[string]any{"members": views}, "Use user_id values from this list in assignee_ids parameters."), nil, nil
	}
}

// ── list_workspaces ──────────────────────────────────────────────────────────

type ListWorkspacesInput struct{}

func listWorkspaces(workspaces workspace.Repository) mcp.ToolHandlerFor[ListWorkspacesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, _ ListWorkspacesInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil, nil
		}
		ws, err := workspaces.ListForUser(ctx, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"workspaces": ws}, "Use workspace slugs from this list in all workspace_slug parameters."), nil, nil
	}
}
