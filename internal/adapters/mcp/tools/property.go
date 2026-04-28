package tools

import (
	"context"
	"encoding/json"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// propertyDefsResp is the response body for tack_list_property_defs.
type propertyDefsResp struct {
	PropertyDefs []*node.PropertyDef `json:"property_defs"`
}

// propsResp is the response body for tack_get_properties. Props mirrors
// Node.Props so each value stays as raw JSON instead of an any soup.
type propsResp struct {
	Props map[string]json.RawMessage `json:"props"`
}

// RegisterProperty registers PropertyDef list/get/create/update/delete.
func RegisterProperty(s *mcpserver.MCPServer, propertyDefs node.PropertyDefRepository, resolver *Resolver) {
	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_list_property_defs",
			Description: "Lists PropertyDef records for the workspace's org.",
			InputSchema: schema{
				Fields: []schemaField{
					{Name: resolver.EntryPointParamName(), Type: schemaString},
				},
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
			defs, err := propertyDefs.List(ctx, ws.OrgID)
			if err != nil {
				return classifyError(ctx, err), nil
			}
			return successJSON(propertyDefsResp{PropertyDefs: defs}, ""), nil
		},
	)

	registerTool(s,
		mcpmcp.Tool{
			Name:        "tack_get_properties",
			Description: "Returns the full Props map for a node.",
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
			return successJSON(propsResp{Props: view.Props}, ""), nil
		},
	)
}
