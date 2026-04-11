package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/node"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterProperty(s *mcp.Server, properties node.PropertyRepository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_property_defs", Description: "Lists custom property definitions for a workspace or project. Returns def ID, name, type, and options."}, listPropertyDefs(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_property_def", Description: "Defines a new custom property on a workspace or project. type can be: text, number, boolean, date, select, multi_select."}, createPropertyDef(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_property_def", Description: "Deletes a custom property definition. This removes all values for this property from all nodes."}, deletePropertyDef(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_set_property", Description: "Sets a custom property value on any node."}, setProperty(properties))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_properties", Description: "Gets all custom property values for a node."}, getProperties(properties))
}

type ListPropertyDefsInput struct {
	OrgID       string `json:"org_id"`
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
}

func listPropertyDefs(properties node.PropertyRepository) mcp.ToolHandlerFor[ListPropertyDefsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListPropertyDefsInput) (*mcp.CallToolResult, any, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		defs, err := properties.ListDefs(ctx, orgID, wsID, parseOptionalUUID(in.ProjectID))
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"defs": defs}, ""), nil, nil
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

func createPropertyDef(properties node.PropertyRepository) mcp.ToolHandlerFor[CreatePropertyDefInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreatePropertyDefInput) (*mcp.CallToolResult, any, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
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
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"def": def}, ""), nil, nil
	}
}

type DeletePropertyDefInput struct {
	OrgID string `json:"org_id"`
	DefID string `json:"def_id"`
}
type DeletePropertyDefOutput struct {
	OK bool `json:"ok"`
}

func deletePropertyDef(properties node.PropertyRepository) mcp.ToolHandlerFor[DeletePropertyDefInput, DeletePropertyDefOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeletePropertyDefInput) (*mcp.CallToolResult, DeletePropertyDefOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), DeletePropertyDefOutput{}, nil
		}
		defID, err := parseUUID(in.DefID, "def_id")
		if err != nil {
			return RecoverableError(err.Error()), DeletePropertyDefOutput{}, nil
		}
		def, err := properties.GetDef(ctx, orgID, defID)
		if err != nil {
			return ClassifyError(ctx, err), DeletePropertyDefOutput{}, nil
		}
		if def == nil {
			return RecoverableError("property def not found"), DeletePropertyDefOutput{}, nil
		}
		if err := properties.DeleteDef(ctx, def); err != nil {
			return ClassifyError(ctx, err), DeletePropertyDefOutput{}, nil
		}
		return Success(DeletePropertyDefOutput{OK: true}, ""), DeletePropertyDefOutput{}, nil
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

func setProperty(properties node.PropertyRepository) mcp.ToolHandlerFor[SetPropertyInput, SetPropertyOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SetPropertyInput) (*mcp.CallToolResult, SetPropertyOutput, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), SetPropertyOutput{}, nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), SetPropertyOutput{}, nil
		}
		propDefID, err := parseUUID(in.PropDefID, "prop_def_id")
		if err != nil {
			return RecoverableError(err.Error()), SetPropertyOutput{}, nil
		}
		if err := properties.SetValue(ctx, orgID, nodeID, propDefID, in.Value); err != nil {
			return ClassifyError(ctx, err), SetPropertyOutput{}, nil
		}
		return Success(SetPropertyOutput{OK: true}, ""), SetPropertyOutput{}, nil
	}
}

type GetPropertiesInput struct {
	OrgID  string `json:"org_id"`
	NodeID string `json:"node_id"`
}

func getProperties(properties node.PropertyRepository) mcp.ToolHandlerFor[GetPropertiesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetPropertiesInput) (*mcp.CallToolResult, any, error) {
		orgID, err := parseUUID(in.OrgID, "org_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		nodeID, err := parseUUID(in.NodeID, "node_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		vals, err := properties.GetValues(ctx, orgID, nodeID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"properties": vals}, ""), nil, nil
	}
}
