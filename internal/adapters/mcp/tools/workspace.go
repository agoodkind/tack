package tools

import (
	"context"
	"encoding/json"
	"fmt"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
)

// nodeTypeSummary is the per-NodeType payload returned by tack_describe_*.
// Mirrors the small subset of NodeType the MCP layer needs (slug, plural,
// human name, features) without exposing the full NodeType struct, which
// includes org-internal fields like ID.
type nodeTypeSummary struct {
	Slug       string
	PluralSlug string
	Name       string
	Features   []string
}

// RegisterWorkspace registers tack_list_workspaces and tack_describe_workspace
// using only generic primitives.
func RegisterWorkspace(s *mcpserver.MCPServer, reader node.NodeReader, resolver *Resolver, nodeTypes []*node.NodeType) {
	registerTool(s,
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

	registerTool(s,
		mcpmcp.Tool{
			Name:        fmt.Sprintf("tack_describe_%s", resolver.entryPointSlug),
			Description: "Describes a workspace: its node types, property defs, and direct children.",
			InputSchema: schema{
				Fields:   []schemaField{{Name: resolver.EntryPointParamName(), Type: schemaString}},
				Required: []string{resolver.EntryPointParamName()},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			slug, ok := requireString(args, resolver.EntryPointParamName())
			if !ok {
				return recoverableError(resolver.EntryPointParamName() + " is required"), nil
			}
			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			parentIDRaw, _ := json.Marshal(ws.ID.String())
			types := make([]nodeTypeSummary, 0, len(nodeTypes))
			for _, nt := range nodeTypes {
				if nt.OrgID != ws.OrgID {
					continue
				}
				types = append(types, nodeTypeSummary{
					Slug:       nt.Slug,
					PluralSlug: nt.PluralSlug,
					Name:       nt.Name,
					Features:   []string(nt.Features),
				})
			}
			children, _ := reader.List(ctx, node.NodeListQuery{
				OrgID: ws.OrgID,
				PropFilters: []node.PropertyMatch{
					{PropName: "parent_id", Value: parentIDRaw},
				},
			})
			rc := newRenderCtxWithTypes(ctx, reader, nil, resolver.typeIndex)
			return successText(renderWorkspaceDescribe(rc, ws, types, children), ""), nil
		},
	)
}

// RegisterMembers registers tack_list_members.
func RegisterMembers(s *mcpserver.MCPServer, members org.MemberRepository, users user.Repository, resolver *Resolver) {
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_list_members",
			Description: "Lists org members for the workspace's org.",
			InputSchema: schema{
				Fields:   []schemaField{{Name: resolver.EntryPointParamName(), Type: schemaString}},
				Required: []string{resolver.EntryPointParamName()},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			slug, ok := requireString(args, resolver.EntryPointParamName())
			if !ok {
				return recoverableError(resolver.EntryPointParamName() + " is required"), nil
			}
			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			ms, err := members.ListMembers(ctx, ws.OrgID)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			usersOut := make([]*user.User, 0, len(ms))
			for _, m := range ms {
				u, err := users.GetByID(ctx, m.UserID)
				if err != nil || u == nil {
					continue
				}
				usersOut = append(usersOut, u)
			}
			return successText(renderMembers(usersOut), ""), nil
		},
	)
}
