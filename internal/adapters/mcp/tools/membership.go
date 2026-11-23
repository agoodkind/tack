package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
)

// RegisterMembers registers tack_list_members.
//
// The tool resolves the workspace reference and returns the users in that
// workspace's organization.
//
//	RegisterMembers(server, members, users, resolver)
func RegisterMembers(
	server *mcpserver.MCPServer,
	members org.MemberRepository,
	users user.Repository,
	resolver *Resolver,
) {
	registerTool(
		server,
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

			entryPointReference, provided := resolver.entryPointReference(args)
			if !provided {
				return recoverableError(resolver.entryPointRequiredMessage()), nil
			}

			workspace, err := resolver.Workspace(ctx, entryPointReference)
			if err != nil {
				return classifyError(ctx, err), nil
			}

			memberships, err := members.ListMembers(ctx, workspace.OrgID)
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

			return successText(renderMembers(usersOut), ""), nil
		},
	)
}
