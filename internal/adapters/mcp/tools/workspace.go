package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
)

// RegisterWorkspace registers tack_list_workspaces and tack_describe_workspace
// using only generic primitives.
func RegisterWorkspace(s *mcpserver.MCPServer, reader node.NodeReader, resolver *Resolver, nodeTypes []*node.NodeType) {
	registerTool(s, 
		mcpmcp.Tool{
			Name:        "tack_list_workspaces",
			Description: "Lists workspaces the caller has access to.",
			InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{}},
		},
		func(ctx context.Context, _ mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			userID, err := mustUser(ctx)
			if err != nil {
				return UnexpectedError(ctx, err), nil
			}
			wss, err := resolver.WorkspacesForUser(ctx, userID)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			return Success(map[string]any{"workspaces": wss}, ""), nil
		},
	)

	registerTool(s, 
		mcpmcp.Tool{
			Name:        fmt.Sprintf("tack_describe_%s", resolver.entryPointSlug),
			Description: "Describes a workspace: its node types, property defs, and direct children.",
			InputSchema: mcpmcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{resolver.EntryPointParamName(): map[string]any{"type": "string"}},
				Required:   []string{resolver.EntryPointParamName()},
			},
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args := req.GetArguments()
			slug, ok := requireString(args, resolver.EntryPointParamName())
			if !ok {
				return RecoverableError(resolver.EntryPointParamName() + " is required"), nil
			}
			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			// Direct children: nodes with Props["parent_id"] == ws.ID.
			parentIDRaw, _ := json.Marshal(ws.ID.String())
			types := make([]map[string]any, 0, len(nodeTypes))
			for _, nt := range nodeTypes {
				if nt.OrgID != ws.OrgID {
					continue
				}
				types = append(types, map[string]any{
					"slug":        nt.Slug,
					"plural_slug": nt.PluralSlug,
					"name":        nt.Name,
					"features":    nt.Features,
				})
			}
			children, _ := reader.List(ctx, node.NodeListQuery{
				OrgID: ws.OrgID,
				PropFilters: []node.PropertyMatch{
					{PropName: "parent_id", Value: parentIDRaw},
				},
			})
			return Success(map[string]any{
				"workspace":  ws,
				"node_types": types,
				"children":   children,
			}, ""), nil
		},
	)
}

// RegisterMembers registers tack_list_members.
func RegisterMembers(s *mcpserver.MCPServer, members org.MemberRepository, users user.Repository, resolver *Resolver) {
	registerTool(s, 
		mcpmcp.Tool{
			Name:        "tack_list_members",
			Description: "Lists org members for the workspace's org.",
			InputSchema: mcpmcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{resolver.EntryPointParamName(): map[string]any{"type": "string"}},
				Required:   []string{resolver.EntryPointParamName()},
			},
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args := req.GetArguments()
			slug, ok := requireString(args, resolver.EntryPointParamName())
			if !ok {
				return RecoverableError(resolver.EntryPointParamName() + " is required"), nil
			}
			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			ms, err := members.ListMembers(ctx, ws.OrgID)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			usersOut := make([]*user.User, 0, len(ms))
			for _, m := range ms {
				u, err := users.GetByID(ctx, m.UserID)
				if err != nil || u == nil {
					continue
				}
				usersOut = append(usersOut, u)
			}
			return Success(map[string]any{"members": usersOut}, ""), nil
		},
	)
}

// Unused helper to silence imports when certain features are disabled.
var _ = uuid.Nil
