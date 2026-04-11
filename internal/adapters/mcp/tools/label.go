package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/label"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterLabel(s *mcp.Server, labels label.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_labels", Description: "Lists labels in a workspace, optionally scoped to a project. Returns label ID, name, and color."}, listLabels(labels))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_label", Description: "Creates a label in a workspace or project. color defaults to #cccccc if omitted."}, createLabel(labels))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_label", Description: "Deletes a label. This removes the label from all nodes it was applied to."}, deleteLabel(labels))
}

type ListLabelsInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   *string `json:"project_id,omitempty"`
}

func listLabels(labels label.Repository) mcp.ToolHandlerFor[ListLabelsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListLabelsInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		var pID *uuid.UUID
		if in.ProjectID != nil {
			pID = parseOptionalUUID(*in.ProjectID)
		}
		ls, err := labels.List(ctx, wsID, pID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"labels": ls}, ""), nil, nil
	}
}

type CreateLabelInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   *string `json:"project_id,omitempty"`
	Name        string  `json:"name"`
	Color       *string `json:"color,omitempty"`
}

func createLabel(labels label.Repository) mcp.ToolHandlerFor[CreateLabelInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateLabelInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		color := "#cccccc"
		if in.Color != nil && *in.Color != "" {
			color = *in.Color
		}
		var projectID *uuid.UUID
		if in.ProjectID != nil {
			projectID = parseOptionalUUID(*in.ProjectID)
		}
		l, err := labels.Create(ctx, &label.Label{
			WorkspaceID: wsID,
			ProjectID:   projectID,
			Name:        in.Name,
			Color:       color,
			SortOrder:   65535,
		})
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"label": l}, ""), nil, nil
	}
}

type DeleteLabelInput struct {
	LabelID string `json:"label_id"`
}
type DeleteLabelOutput struct {
	OK bool `json:"ok"`
}

func deleteLabel(labels label.Repository) mcp.ToolHandlerFor[DeleteLabelInput, DeleteLabelOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteLabelInput) (*mcp.CallToolResult, DeleteLabelOutput, error) {
		id, err := parseUUID(in.LabelID, "label_id")
		if err != nil {
			return RecoverableError(err.Error()), DeleteLabelOutput{}, nil
		}
		if err := labels.Delete(ctx, id); err != nil {
			return ClassifyError(ctx, err), DeleteLabelOutput{}, nil
		}
		return Success(DeleteLabelOutput{OK: true}, ""), DeleteLabelOutput{}, nil
	}
}
