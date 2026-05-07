package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterProperty registers tools for property metadata and raw node props.
func RegisterProperty(s *mcpserver.MCPServer, propertyDefs node.PropertyDefRepository, resolver *Resolver) {
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_list_property_defs",
			Description: "Lists property definitions for the workspace org as Markdown.",
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
			defs, err := propertyDefs.List(ctx, ws.OrgID)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			return successText(renderPropertyDefs(defs), ""), nil
		},
	)

	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_get_properties",
			Description: "Returns the raw Props map for a node as fenced JSON Markdown.",
			InputSchema: schema{
				Fields:   []schemaField{{Name: "node_id", Type: schemaString}},
				Required: []string{"node_id"},
			}.toMCP(),
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args, err := bindArgs(req)
			if err != nil {
				return recoverableError(err.Error()), nil
			}
			input, ok := requireString(args, "node_id")
			if !ok {
				return recoverableError("node_id is required"), nil
			}
			id, err := resolver.ResolveNodeID(ctx, input)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			view, err := resolver.reader.Get(ctx, id)
			if err != nil || view == nil {
				return classifyError(ctx, err), nil
			}
			stampAuditNodeView(ctx, view)
			rc := newRenderCtxWithTypes(ctx, resolver.reader, nil, resolver.typeIndex)
			return successText(renderProperties(rc, view), ""), nil
		},
	)
}
