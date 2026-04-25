package tools

import (
	"context"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterProperty registers PropertyDef list/get/create/update/delete.
func RegisterProperty(s *mcpserver.MCPServer, propertyDefs node.PropertyDefRepository, resolver *Resolver) {
	registerTool(s, 
		mcpmcp.Tool{
			Name:        "tack_list_property_defs",
			Description: "Lists PropertyDef records for the workspace's org.",
			InputSchema: mcpmcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{resolver.EntryPointParamName(): map[string]any{"type": "string"}},
				Required:   []string{resolver.EntryPointParamName()},
			},
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args := req.GetArguments()
			slug, _ := args[resolver.EntryPointParamName()].(string)
			ws, err := resolver.Workspace(ctx, slug)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			defs, err := propertyDefs.List(ctx, ws.OrgID)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			return Success(map[string]any{"property_defs": defs}, ""), nil
		},
	)

	registerTool(s, 
		mcpmcp.Tool{
			Name:        "tack_get_properties",
			Description: "Returns the full Props map for a node.",
			InputSchema: mcpmcp.ToolInputSchema{
				Type:       "object",
				Properties: map[string]any{"node_id": map[string]any{"type": "string"}},
				Required:   []string{"node_id"},
			},
		},
		func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
			args := req.GetArguments()
			input, _ := args["node_id"].(string)
			id, err := resolver.ResolveNodeID(ctx, input)
			if err != nil {
				return ClassifyError(ctx, err), nil
			}
			view, err := resolver.reader.Get(ctx, id)
			if err != nil || view == nil {
				return ClassifyError(ctx, err), nil
			}
			return Success(map[string]any{"props": view.Props}, ""), nil
		},
	)
}

// Unused import silencer when property_defs skip uuid.
var _ = uuid.Nil
