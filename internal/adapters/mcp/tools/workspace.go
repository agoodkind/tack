package tools

import (
	"context"
	"encoding/json"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
)

// nodeTypeSummary is the per-NodeType payload returned by tack_describe_*.
// Mirrors the small subset of NodeType the MCP layer needs for command names,
// human names, features, and declared reference contracts without exposing the full NodeType struct, which
// includes org-internal fields like ID.
type nodeTypeSummary struct {
	Slug       string
	PluralSlug string
	Name       string
	Features   []string
	Reference  node.ReferenceConfig
	// ChildCount is the number of direct children of this type under the
	// described workspace, read one page deep. ChildCountMore is true when
	// more than ChildCount exist.
	ChildCount     int
	ChildCountMore bool
}

// RegisterWorkspace registers tack_list_workspaces and tack_describe_workspace
// using only generic primitives.
func RegisterWorkspace(
	s *mcpserver.MCPServer,
	reader node.NodeReader,
	resolver *Resolver,
	nodeTypes []*node.NodeType,
) {
	registerTool(
		s,
		mcpmcp.Tool{
			Name:        "tack_list_workspaces",
			Description: "Lists workspaces the caller has access to.",
			InputSchema: schema{}.toMCP(),
		},
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			userID, err := mustUser(ctx)
			if err != nil {
				return unexpectedError(ctx, err), nil
			}

			wss, err := resolver.WorkspacesForUser(ctx, userID)
			if err != nil {
				return classifyError(ctx, err), nil
			}

			rc := newRenderCtxWithTypes(ctx, reader, nil, resolver.typeIndex)

			return successText(renderList(rc, "workspaces", wss), ""), nil
		},
	)

	registerWorkspaceDescription(s, reader, resolver, nodeTypes)
}

// RegisterMembers registers tack_list_members.
func RegisterMembers(
	s *mcpserver.MCPServer,
	members org.MemberRepository,
	users user.Repository,
	resolver *Resolver,
) {
	registerTool(
		s,
		mcpmcp.Tool{
			Name:        "tack_list_members",
			Description: "Lists org members for the workspace's org.",
			InputSchema: schema{
				Fields:   entryPointSchemaFields(resolver),
				Required: []string{resolver.EntryPointParamName()},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}

			entryPointReference, ok := resolver.entryPointReference(args)
			if !ok {
				return recoverableError(resolver.entryPointRequiredMessage()), nil
			}

			ws, err := resolver.Workspace(ctx, entryPointReference)
			if err != nil {
				return classifyError(ctx, err), nil
			}

			memberships, err := members.ListMembers(ctx, ws.OrgID)
			if err != nil {
				return classifyError(ctx, err), nil
			}

			usersOut := make([]*user.User, 0, len(memberships))
			for _, m := range memberships {
				u, err := users.GetByID(ctx, m.UserID)
				if err != nil || u == nil {
					continue
				}

				usersOut = append(usersOut, u)
			}

			addUserTargets(ctx, ws.OrgID, usersOut)

			return successText(renderMembers(usersOut), ""), nil
		},
	)
}

func registerWorkspaceDescription(
	server *mcpserver.MCPServer,
	reader node.NodeReader,
	resolver *Resolver,
	nodeTypes []*node.NodeType,
) {
	registerTool(
		server,
		mcpmcp.Tool{
			Name:        "tack_describe_" + resolver.entryPointSlug,
			Description: "Describes a workspace: its node types and a count of direct children per node type.",
			InputSchema: schema{
				Fields:   entryPointSchemaFields(resolver),
				Required: []string{resolver.EntryPointParamName()},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}

			entryPointReference, ok := resolver.entryPointReference(args)
			if !ok {
				return recoverableError(resolver.entryPointRequiredMessage()), nil
			}

			workspace, err := resolver.Workspace(ctx, entryPointReference)
			if err != nil {
				return classifyError(ctx, err), nil
			}

			parentIDRaw, _ := json.Marshal(workspace.ID.String())

			types := make([]nodeTypeSummary, 0, len(nodeTypes))
			for _, nodeType := range nodeTypes {
				if nodeType.OrgID != workspace.OrgID {
					continue
				}

				page, err := reader.ListPage(
					ctx,
					node.NodeListQuery{
						OrgID:    workspace.OrgID,
						NodeType: nodeType.TypeKey,
						ByProperty: &node.PropertyMatch{
							PropName: parentIDPropertyName,
							Value:    parentIDRaw,
						},
						BySourceRelation: nil,
						ByTargetRelation: nil,
						CreatedAfter:     nil,
						CreatedBefore:    nil,
						PropFilters:      nil,
						Limit:            maxListLimit,
						Cursor:           "",
					},
				)
				if err != nil {
					return classifyError(ctx, err), nil
				}

				types = append(
					types,
					nodeTypeSummary{
						Slug:           nodeType.Slug,
						PluralSlug:     nodeType.PluralSlug,
						Name:           nodeType.Name,
						Features:       []string(nodeType.Features),
						Reference:      nodeType.Reference,
						ChildCount:     len(page.Views),
						ChildCountMore: page.NextCursor != "",
					},
				)
			}

			rc := newRenderCtxWithTypes(ctx, reader, nil, resolver.typeIndex)

			return successText(renderWorkspaceDescribe(rc, workspace, types), ""), nil
		},
	)
}
