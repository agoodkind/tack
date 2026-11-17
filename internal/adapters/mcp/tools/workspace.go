package tools

import (
	"context"
	"fmt"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	mcpserver "github.com/mark3labs/mcp-go/v2/server"
	"goodkind.io/tack/internal/domain/node"
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
	registerWorkspaceList(s, reader, resolver)

	registerTool(
		s,
		mcpmcp.Tool{
			Name:        fmt.Sprintf("tack_describe_%s", resolver.entryPointSlug),
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

			ws, err := resolver.Workspace(ctx, entryPointReference)
			if err != nil {
				return classifyError(ctx, err), nil
			}

			types := make([]nodeTypeSummary, 0, len(nodeTypes))
			for _, nt := range nodeTypes {
				if nt.OrgID != ws.OrgID {
					continue
				}

				page, err := reader.ListPage(
					ctx,
					node.NodeListQuery{
						OrgID:      ws.OrgID,
						NodeType:   nt.TypeKey,
						ByProperty: nil,
						ByTargetRelation: &node.RelationScan{
							NodeID:       ws.ID,
							RelationType: node.RelChildOf,
						},
						BySourceRelation: nil,
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
						Slug:           nt.Slug,
						PluralSlug:     nt.PluralSlug,
						Name:           nt.Name,
						Features:       []string(nt.Features),
						Reference:      nt.Reference,
						ChildCount:     len(page.Views),
						ChildCountMore: page.NextCursor != "",
					},
				)
			}

			rc := newRenderCtxWithTypes(ctx, reader, nil, resolver.typeIndex)

			return successText(renderWorkspaceDescribe(rc, ws, types), ""), nil
		},
	)
}
