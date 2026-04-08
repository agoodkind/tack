package tools

import (
	"context"
	"encoding/json"

	"github.com/agoodkind/tack/internal/domain/node"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterProperty(s *mcp.Server, properties node.PropertyRepository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_property_defs", Description: "List custom property definitions for a workspace or project"}, listPropertyDefs(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_property_def", Description: "Define a new custom property on a workspace or project"}, createPropertyDef(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_property_def", Description: "Delete a custom property definition"}, deletePropertyDef(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_set_property", Description: "Set a custom property value on any node"}, setProperty(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_properties", Description: "Get all custom property values for a node"}, getProperties(properties))
}

type ListPropertyDefsInput struct {
	OrgID       string `json:"org_id"       jsonschema:"required"`
	WorkspaceID string `json:"workspace_id" jsonschema:"required"`
	ProjectID   string `json:"project_id"  `
}
type ListPropertyDefsOutput struct {
	Defs json.RawMessage `json:"defs"`
}

func listPropertyDefs(properties node.PropertyRepository) mcp.ToolHandlerFor[ListPropertyDefsInput, ListPropertyDefsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListPropertyDefsInput) (*mcp.CallToolResult, ListPropertyDefsOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, ListPropertyDefsOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, ListPropertyDefsOutput{}, err
		}
		defs, err := properties.ListDefs(ctx, orgID, wsID, parseOptionalUUID(in.ProjectID))
		if err != nil {
			return nil, ListPropertyDefsOutput{}, err
		}
		b, _ := json.Marshal(defs)
		return nil, ListPropertyDefsOutput{Defs: b}, nil
	}
}

type CreatePropertyDefInput struct {
	OrgID       string `json:"org_id"         jsonschema:"required"`
	WorkspaceID string `json:"workspace_id"  `
	ProjectID   string `json:"project_id"    `
	Name        string `json:"name"           jsonschema:"required"`
	Type        string `json:"type"           jsonschema:"required"`
	Options     []string `json:"options"     `
	Required    bool   `json:"required"      `
}
type CreatePropertyDefOutput struct {
	Def json.RawMessage `json:"def"`
}

func createPropertyDef(properties node.PropertyRepository) mcp.ToolHandlerFor[CreatePropertyDefInput, CreatePropertyDefOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreatePropertyDefInput) (*mcp.CallToolResult, CreatePropertyDefOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, CreatePropertyDefOutput{}, err
		}
		def := &node.PropertyDef{
			ID:          uuid.New(),
			OrgID:       orgID,
			WorkspaceID: parseOptionalUUID(in.WorkspaceID),
			ProjectID:   parseOptionalUUID(in.ProjectID),
			Name:        in.Name,
			Type:        node.PropertyType(in.Type),
			Options:     in.Options,
			Required:    in.Required,
		}
		if err := properties.SetDef(ctx, def); err != nil {
			return nil, CreatePropertyDefOutput{}, err
		}
		b, _ := json.Marshal(def)
		return nil, CreatePropertyDefOutput{Def: b}, nil
	}
}

type DeletePropertyDefInput struct {
	OrgID string `json:"org_id" jsonschema:"required"`
	DefID string `json:"def_id" jsonschema:"required"`
}
type DeletePropertyDefOutput struct {
	OK bool `json:"ok"`
}

func deletePropertyDef(properties node.PropertyRepository) mcp.ToolHandlerFor[DeletePropertyDefInput, DeletePropertyDefOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeletePropertyDefInput) (*mcp.CallToolResult, DeletePropertyDefOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, DeletePropertyDefOutput{}, err
		}
		defID, err := parseUUID(in.DefID, "def_id")
		if err != nil {
			return nil, DeletePropertyDefOutput{}, err
		}
		def, err := properties.GetDef(ctx, orgID, defID)
		if err != nil {
			return nil, DeletePropertyDefOutput{}, err
		}
		if def == nil {
			return nil, DeletePropertyDefOutput{}, errNotFound("property def")
		}
		if err := properties.DeleteDef(ctx, def); err != nil {
			return nil, DeletePropertyDefOutput{}, err
		}
		return nil, DeletePropertyDefOutput{OK: true}, nil
	}
}

type SetPropertyInput struct {
	OrgID     string `json:"org_id"      jsonschema:"required"`
	NodeID    string `json:"node_id"     jsonschema:"required"`
	PropDefID string `json:"prop_def_id" jsonschema:"required"`
	Value     any    `json:"value"       jsonschema:"required"`
}
type SetPropertyOutput struct {
	OK bool `json:"ok"`
}

func setProperty(properties node.PropertyRepository) mcp.ToolHandlerFor[SetPropertyInput, SetPropertyOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SetPropertyInput) (*mcp.CallToolResult, SetPropertyOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, SetPropertyOutput{}, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, SetPropertyOutput{}, err
		}
		propDefID, err := parseUUID(in.PropDefID, "prop_def_id")
		if err != nil {
			return nil, SetPropertyOutput{}, err
		}
		if err := properties.SetValue(ctx, orgID, nodeID, propDefID, in.Value); err != nil {
			return nil, SetPropertyOutput{}, err
		}
		return nil, SetPropertyOutput{OK: true}, nil
	}
}

type GetPropertiesInput struct {
	OrgID  string `json:"org_id"  jsonschema:"required"`
	NodeID string `json:"node_id" jsonschema:"required"`
}
type GetPropertiesOutput struct {
	Properties json.RawMessage `json:"properties"`
}

func getProperties(properties node.PropertyRepository) mcp.ToolHandlerFor[GetPropertiesInput, GetPropertiesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetPropertiesInput) (*mcp.CallToolResult, GetPropertiesOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return nil, GetPropertiesOutput{}, err
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return nil, GetPropertiesOutput{}, err
		}
		vals, err := properties.GetValues(ctx, orgID, nodeID)
		if err != nil {
			return nil, GetPropertiesOutput{}, err
		}
		b, _ := json.Marshal(vals)
		return nil, GetPropertiesOutput{Properties: b}, nil
	}
}
