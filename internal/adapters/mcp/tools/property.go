package tools

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/telemetry"
)

func RegisterProperty(s *mcpserver.MCPServer, properties node.PropertyRepository) {
	s.AddTool(mcpmcp.Tool{Name: "tack_list_property_defs", Description: "Lists custom property definitions for a workspace or project. Returns def ID, name, type, and options.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"org_id": map[string]any{"type": "string", "description": "The org ID"}, "workspace_id": map[string]any{"type": "string", "description": "The workspace ID"}, "project_id": map[string]any{"type": "string", "description": "The project ID"}}, Required: []string{"org_id", "workspace_id", "project_id"}}}, listPropertyDefs(properties))
	s.AddTool(mcpmcp.Tool{Name: "tack_create_property_def", Description: "Defines a new custom property on a workspace or project. type can be: text, number, boolean, date, select, multi_select.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"org_id": map[string]any{"type": "string", "description": "The org ID"}, "workspace_id": map[string]any{"type": "string", "description": "The workspace ID"}, "project_id": map[string]any{"type": "string", "description": "The project ID"}, "name": map[string]any{"type": "string", "description": "The property name"}, "type": map[string]any{"type": "string", "description": "The property type"}, "options": map[string]any{"type": "array", "description": "The property options"}, "required": map[string]any{"type": "boolean", "description": "Whether the property is required"}}, Required: []string{"org_id", "workspace_id", "project_id", "name", "type"}}}, createPropertyDef(properties))
	s.AddTool(mcpmcp.Tool{Name: "tack_delete_property_def", Description: "Deletes a custom property definition. This removes all values for this property from all nodes.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"org_id": map[string]any{"type": "string", "description": "The org ID"}, "def_id": map[string]any{"type": "string", "description": "The def ID"}}, Required: []string{"org_id", "def_id"}}}, deletePropertyDef(properties))
	s.AddTool(mcpmcp.Tool{Name: "tack_set_property", Description: "Sets a custom property value on any node.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"org_id": map[string]any{"type": "string", "description": "The org ID"}, "node_id": map[string]any{"type": "string", "description": "The node ID"}, "prop_def_id": map[string]any{"type": "string", "description": "The prop def ID"}, "value": map[string]any{"type": "string", "description": "The value"}}, Required: []string{"org_id", "node_id", "prop_def_id"}}}, setProperty(properties))
	s.AddTool(mcpmcp.Tool{Name: "tack_get_properties", Description: "Gets all custom property values for a node.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"org_id": map[string]any{"type": "string", "description": "The org ID"}, "node_id": map[string]any{"type": "string", "description": "The node ID"}}, Required: []string{"org_id", "node_id"}}}, getProperties(properties))
}

type ListPropertyDefsInput struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
}

func listPropertyDefs(properties node.PropertyRepository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListPropertyDefsInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.list_property_defs", slog.String("workspace_id", in.WorkspaceID))
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		defs, err := properties.ListDefs(ctx, orgID, wsID, parseOptionalUUID(in.ProjectID))
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"defs": defs}, ""), nil
	}
}

type CreatePropertyDefInput struct {
	OrgID       string   `json:"org_id"`
	WorkspaceID string   `json:"workspace_id"`
	ProjectID   string   `json:"project_id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Options     []string `json:"options,omitempty"`
	Required    bool     `json:"required,omitempty"`
}

func createPropertyDef(properties node.PropertyRepository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreatePropertyDefInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.create_property_def", slog.String("name", in.Name), slog.String("type", in.Type))
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		opts := make([]node.EnumOption, len(in.Options))
		for i, o := range in.Options {
			opts[i] = node.EnumOption{Key: o, Label: o, SortRank: int32(i)}
		}
		def := &node.PropertyDef{
			ID:          uuid.New(),
			OrgID:       orgID,
			WorkspaceID: parseOptionalUUID(in.WorkspaceID),
			ProjectID:   parseOptionalUUID(in.ProjectID),
			Name:        in.Name,
			Type:        node.PropertyType(in.Type),
			Options:     opts,
			Required:    in.Required,
		}
		if err := properties.SetDef(ctx, def); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"def": def}, ""), nil
	}
}

type DeletePropertyDefInput struct {
	OrgID string `json:"org_id"`
	DefID string `json:"def_id"`
}
type DeletePropertyDefOutput struct {
	OK bool `json:"ok"`
}

func deletePropertyDef(properties node.PropertyRepository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DeletePropertyDefInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.delete_property_def", slog.String("def_id", in.DefID))
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		defID, err := parseUUID(in.DefID, "def_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		def, err := properties.GetDef(ctx, orgID, defID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if def == nil {
			return RecoverableError("property def not found"), nil
		}
		if err := properties.DeleteDef(ctx, def); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(DeletePropertyDefOutput{OK: true}, ""), nil
	}
}

type SetPropertyInput struct {
	OrgID     string `json:"org_id"`
	NodeID    string `json:"node_id"`
	PropDefID string `json:"prop_def_id"`
	Value     string `json:"value,omitempty"`
}
type SetPropertyOutput struct {
	OK bool `json:"ok"`
}

func setProperty(properties node.PropertyRepository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in SetPropertyInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.set_property", slog.String("node_id", in.NodeID), slog.String("prop_def_id", in.PropDefID))
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		propDefID, err := parseUUID(in.PropDefID, "prop_def_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		if err := properties.SetValue(ctx, orgID, nodeID, propDefID, in.Value); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(SetPropertyOutput{OK: true}, ""), nil
	}
}

type GetPropertiesInput struct {
	OrgID  string `json:"org_id"`
	NodeID string `json:"node_id"`
}

func getProperties(properties node.PropertyRepository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in GetPropertiesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		telemetry.L(ctx).Debug("mcp.get_properties", slog.String("node_id", in.NodeID))
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		vals, err := properties.GetValues(ctx, orgID, nodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"properties": vals}, ""), nil
	}
}
