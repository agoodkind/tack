package tools

import (
	"context"
	"encoding/json"

	"github.com/agoodkind/tack/internal/domain/label"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterLabel(s *mcp.Server, labels label.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_labels", Description: "List labels for a workspace (optionally scoped to a project)"}, listLabels(labels))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_label", Description: "Create a label in a workspace or project"}, createLabel(labels))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_label", Description: "Delete a label"}, deleteLabel(labels))
}

type ListLabelsInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	ProjectID   string `json:"project_id"   jsonschema:"description=Optional project UUID to include project-scoped labels"`
}
type ListLabelsOutput struct {
	Labels json.RawMessage `json:"labels"`
}

func listLabels(labels label.Repository) mcp.ToolHandlerFor[ListLabelsInput, ListLabelsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListLabelsInput) (*mcp.CallToolResult, ListLabelsOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, ListLabelsOutput{}, err
		}
		pID := parseOptionalUUID(in.ProjectID)
		ls, err := labels.List(ctx, wsID, pID)
		if err != nil {
			return nil, ListLabelsOutput{}, err
		}
		b, _ := json.Marshal(ls)
		return nil, ListLabelsOutput{Labels: b}, nil
	}
}

type CreateLabelInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	ProjectID   string `json:"project_id"   jsonschema:"description=Optional project UUID (omit for workspace-scoped label)"`
	Name        string `json:"name"         jsonschema:"required,description=Label name"`
	Color       string `json:"color"        jsonschema:"description=Hex color e.g. #ff0000"`
}
type CreateLabelOutput struct {
	Label json.RawMessage `json:"label"`
}

func createLabel(labels label.Repository) mcp.ToolHandlerFor[CreateLabelInput, CreateLabelOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateLabelInput) (*mcp.CallToolResult, CreateLabelOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, CreateLabelOutput{}, err
		}
		color := in.Color
		if color == "" {
			color = "#cccccc"
		}
		l, err := labels.Create(ctx, &label.Label{
			WorkspaceID: wsID,
			ProjectID:   parseOptionalUUID(in.ProjectID),
			Name:        in.Name,
			Color:       color,
			SortOrder:   65535,
		})
		if err != nil {
			return nil, CreateLabelOutput{}, err
		}
		b, _ := json.Marshal(l)
		return nil, CreateLabelOutput{Label: b}, nil
	}
}

type DeleteLabelInput struct {
	LabelID string `json:"label_id" jsonschema:"required,description=Label UUID"`
}
type DeleteLabelOutput struct {
	OK bool `json:"ok"`
}

func deleteLabel(labels label.Repository) mcp.ToolHandlerFor[DeleteLabelInput, DeleteLabelOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteLabelInput) (*mcp.CallToolResult, DeleteLabelOutput, error) {
		id, err := parseUUID(in.LabelID, "label_id")
		if err != nil {
			return nil, DeleteLabelOutput{}, err
		}
		if err := labels.Delete(ctx, id); err != nil {
			return nil, DeleteLabelOutput{}, err
		}
		return nil, DeleteLabelOutput{OK: true}, nil
	}
}
