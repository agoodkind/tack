package tools

import (
	"context"
	"sort"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
)

func RegisterWorkspace(
	s *mcpserver.MCPServer,
	entities node.EntityRepository,
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
	r *Resolver,
) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_describe_workspace",
		Description: "Introspects a workspace: returns projects with states, node types, and property definitions. Call this first before any other workspace-scoped operation.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug": map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug"},
		},
	}, describeWorkspace(reader, nodeTypes, properties, r))

	s.AddTool(mcpmcp.Tool{
		Name:        "tack_list_workspaces",
		Description: "Lists all workspaces the authenticated user has access to. Returns workspace slug, name, and ID. Use the slug in all workspace_slug parameters.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
			Required:   []string{},
		},
	}, listWorkspaces(r))
}

// ── describe_workspace ───────────────────────────────────────────────────────

type DescribeWorkspaceInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
}

func describeWorkspace(
	reader node.NodeReader,
	nodeTypes node.TypeRepository,
	properties node.PropertyRepository,
	r *Resolver,
) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DescribeWorkspaceInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}

		projViews, err := reader.List(ctx, node.NodeListQuery{
			OrgID:       ws.OrgID,
			WorkspaceID: ws.ID,
			NodeType:    node.NodeTypeProject,
		})
		if err != nil {
			return ClassifyError(ctx, err), nil
		}

		type projectSummary struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Identifier string `json:"identifier"`
			States     any    `json:"states"`
		}
		summaries := make([]projectSummary, 0, len(projViews))
		for _, p := range projViews {
			ss := listStateViewsByProject(ctx, reader, ws.OrgID, ws.ID, p.ID)
			summaries = append(summaries, projectSummary{
				ID:         p.ID.String(),
				Name:       p.Name,
				Identifier: ViewIdentifier(p),
				States:     ss,
			})
		}

		nts, _ := nodeTypes.List(ctx, ws.OrgID)
		defs, _ := properties.ListDefs(ctx, ws.OrgID, ws.ID, nil)

		return Success(map[string]any{
			"workspace":            ws,
			"projects":             summaries,
			"node_types":           nts,
			"property_definitions": defs,
		}, "Use the project identifiers here as project_identifier in tack_list_issues and related tools."), nil
	}
}

// RegisterMembers registers the tack_list_members tool.
func RegisterMembers(s *mcpserver.MCPServer, members org.MemberRepository, users user.Repository, r *Resolver) {
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_list_members",
		Description: "Lists all members of a workspace with display name and email. Returns user_id, display_name, email, and role. Use this to look up user IDs before assigning issues.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				"workspace_slug": map[string]any{"type": "string"},
			},
			Required: []string{"workspace_slug"},
		},
	}, listMembers(members, users, r))
}

// ── list_members ─────────────────────────────────────────────────────────────

type ListMembersInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
}

func listMembers(members org.MemberRepository, users user.Repository, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListMembersInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, err := r.Workspace(ctx, in.WorkspaceSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		membersList, err := members.ListMembers(ctx, ws.OrgID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		type memberView struct {
			UserID      string `json:"user_id"`
			DisplayName string `json:"display_name"`
			Email       string `json:"email"`
			Role        int    `json:"role"`
		}
		views := make([]memberView, 0, len(membersList))
		for _, m := range membersList {
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
		return Success(map[string]any{"members": views}, "Use user_id values from this list in assignee_ids parameters."), nil
	}
}

// ── list_workspaces ──────────────────────────────────────────────────────────

type ListWorkspacesInput struct{}

func listWorkspaces(r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListWorkspacesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, err := r.WorkspacesForUser(ctx, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"workspaces": ws}, "Use workspace slugs from this list in all workspace_slug parameters."), nil
	}
}

// listStateViewsByProject lists state NodeListViews for a project, sorted by sort_order.
// Shared by workspace, project, and resource handlers.
func listStateViewsByProject(ctx context.Context, reader node.NodeReader, orgID, wsID, projID uuid.UUID) []*node.NodeListView {
	views, _ := reader.List(ctx, node.NodeListQuery{
		OrgID:       orgID,
		WorkspaceID: wsID,
		NodeType:    node.NodeTypeState,
		ByProject:   &projID,
	})
	sort.Slice(views, func(i, j int) bool {
		return views[i].SortOrder < views[j].SortOrder
	})
	return views
}
