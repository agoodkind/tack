package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/telemetry"
)

func RegisterWorkspace(
	s *mcpserver.MCPServer,
	reader node.NodeReader,
	r *Resolver,
	allTypes []*node.NodeType,
) {
	epParam := r.EntryPointParamName()
	// Find the entry point type to derive tool names from its slug.
	var epSlug, epPlural, epName string
	for _, nt := range allTypes {
		if nt.Features.Has(node.FeatureIsEntryPoint) {
			epSlug = strings.ToLower(nt.Slug)
			epPlural = strings.ToLower(nt.PluralSlug)
			epName = nt.Name
			if epPlural == "" {
				epPlural = epSlug + "s"
			}
			break
		}
	}

	s.AddTool(mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_describe_%s", epSlug),
		Description: fmt.Sprintf("Introspects a %s: returns containers with their children, node types, and property definitions. Call this first before any other scoped operation.", epName),
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				epParam: map[string]any{"type": "string"},
			},
			Required: []string{epParam},
		},
	}, describeWorkspace(reader, r, allTypes, epParam))

	s.AddTool(mcpmcp.Tool{
		Name:        fmt.Sprintf("tack_list_%s", epPlural),
		Description: fmt.Sprintf("Lists all %s the authenticated user has access to. Returns slug, name, and ID.", epPlural),
		InputSchema: mcpmcp.ToolInputSchema{
			Type:       "object",
			Properties: map[string]any{},
			Required:   []string{},
		},
	}, listWorkspaces(r))
}

// ── describe_workspace ───────────────────────────────────────────────────────

func describeWorkspace(
	reader node.NodeReader,
	r *Resolver,
	allTypes []*node.NodeType,
	epParam string,
) mcpserver.ToolHandlerFunc {
	// Build a type index to discover what lives under each container.
	typeIndex := node.BuildTypeIndex(allTypes)

	// Find the entry point type to discover its CanContain children.
	var entryPointType *node.NodeType
	for _, nt := range allTypes {
		if nt.Features.Has(node.FeatureIsEntryPoint) {
			entryPointType = nt
			break
		}
	}

	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		epSlug, _ := req.GetArguments()[epParam].(string)
		telemetry.L(ctx).Debug("mcp.describe", slog.String("entry_point", epSlug))
		ws, err := r.Workspace(ctx, epSlug)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}

		result := map[string]any{"entry_point": ws}

		if entryPointType != nil {
			addContainerChildren(ctx, reader, ws.OrgID, ws.ID, uuid.Nil, entryPointType, typeIndex, result)
		}

		return Success(result, "Use identifiers from this response in tool calls."), nil
	}
}

// RegisterMembers registers the tack_list_members tool.
func RegisterMembers(s *mcpserver.MCPServer, members org.MemberRepository, users user.Repository, r *Resolver) {
	epParam := r.EntryPointParamName()
	s.AddTool(mcpmcp.Tool{
		Name:        "tack_list_members",
		Description: "Lists all members with display name and email. Returns user_id, display_name, email, and role.",
		InputSchema: mcpmcp.ToolInputSchema{
			Type: "object",
			Properties: map[string]any{
				epParam: map[string]any{"type": "string"},
			},
			Required: []string{epParam},
		},
	}, listMembers(members, users, r, epParam))
}

// ── list_members ─────────────────────────────────────────────────────────────

func listMembers(members org.MemberRepository, users user.Repository, r *Resolver, epParam string) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		epSlug, _ := req.GetArguments()[epParam].(string)
		telemetry.L(ctx).Debug("mcp.list_members", slog.String("entry_point", epSlug))
		ws, err := r.Workspace(ctx, epSlug)
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

func listWorkspaces(r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		telemetry.L(ctx).Debug("mcp.list_entry_points")
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, err := r.WorkspacesForUser(ctx, userID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"entry_points": ws}, fmt.Sprintf("Use slugs from this list as %s in tool calls.", r.EntryPointParamName())), nil
	}
}

// listChildrenByType lists NodeListViews of a given type under a parent container, sorted by sort_order.
func listChildrenByType(ctx context.Context, reader node.NodeReader, orgID, wsID, parentID uuid.UUID, nodeType string) []*node.NodeListView {
	views, _ := reader.List(ctx, node.NodeListQuery{
		OrgID:       orgID,
		WorkspaceID: wsID,
		NodeType:    nodeType,
		ByProject:   &parentID,
	})
	sort.Slice(views, func(i, j int) bool {
		return views[i].SortOrder < views[j].SortOrder
	})
	return views
}

// containerSummary is the describe response shape for a container node.
type containerSummary struct {
	ID         string                   `json:"id"`
	Name       string                   `json:"name"`
	Identifier string                   `json:"identifier,omitempty"`
	TypeKey    string                   `json:"type_key"`
	Children   map[string]any           `json:"children,omitempty"`
}

// addContainerChildren recursively walks CanContain to build the describe tree.
// parentID is uuid.Nil when listing direct children of the entry point (workspace-scoped).
// Results are added to the result map keyed by the child type's plural slug.
func addContainerChildren(
	ctx context.Context,
	reader node.NodeReader,
	orgID, wsID, parentID uuid.UUID,
	parentType *node.NodeType,
	typeIndex map[string]*node.NodeType,
	result map[string]any,
) {
	for _, childTypeKey := range parentType.CanContain {
		childType := typeIndex[childTypeKey]
		if childType == nil {
			continue
		}

		q := node.NodeListQuery{OrgID: orgID, WorkspaceID: wsID, NodeType: childTypeKey}
		if parentID != uuid.Nil {
			q.ByProject = &parentID
		}
		childViews, err := reader.List(ctx, q)
		if err != nil {
			continue
		}

		summaries := make([]containerSummary, 0, len(childViews))
		for _, cv := range childViews {
			s := containerSummary{
				ID:         cv.ID.String(),
				Name:       cv.Name,
				Identifier: ViewIdentifier(cv),
				TypeKey:    childTypeKey,
			}
			// Recurse into containers.
			if childType.Features.Has(node.FeatureIsContainer) && len(childType.CanContain) > 0 {
				s.Children = make(map[string]any)
				addContainerChildren(ctx, reader, orgID, wsID, cv.ID, childType, typeIndex, s.Children)
			}
			summaries = append(summaries, s)
		}

		plural := childType.PluralSlug
		if plural == "" {
			plural = childType.Slug + "s"
		}
		result[plural] = summaries
	}
}
