package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/label"
	"github.com/google/uuid"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func RegisterLabel(s *mcpserver.MCPServer, labels label.Repository) {
	s.AddTool(mcpmcp.Tool{Name: "tack_list_labels", Description: "Lists labels in a workspace, optionally scoped to a project. Returns label ID, name, and color.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_id": map[string]any{"type": "string", "description": "The workspace ID"}, "project_id": map[string]any{"type": "string", "description": "The project ID"}}, Required: []string{"workspace_id"}}}, listLabels(labels))
	s.AddTool(mcpmcp.Tool{Name: "tack_create_label", Description: "Creates a label in a workspace or project. color defaults to #cccccc if omitted.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_id": map[string]any{"type": "string", "description": "The workspace ID"}, "project_id": map[string]any{"type": "string", "description": "The project ID"}, "name": map[string]any{"type": "string", "description": "The label name"}, "color": map[string]any{"type": "string", "description": "The label color"}}, Required: []string{"workspace_id", "name"}}}, createLabel(labels))
	s.AddTool(mcpmcp.Tool{Name: "tack_delete_label", Description: "Deletes a label. This removes the label from all nodes it was applied to.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"label_id": map[string]any{"type": "string", "description": "The label ID"}}, Required: []string{"label_id"}}}, deleteLabel(labels))
}

type ListLabelsInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   *string `json:"project_id,omitempty"`
}

func listLabels(labels label.Repository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListLabelsInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		var pID *uuid.UUID
		if in.ProjectID != nil {
			pID = parseOptionalUUID(*in.ProjectID)
		}
		ls, err := labels.List(ctx, wsID, pID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"labels": ls}, ""), nil
	}
}

type CreateLabelInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   *string `json:"project_id,omitempty"`
	Name        string  `json:"name"`
	Color       *string `json:"color,omitempty"`
}

func createLabel(labels label.Repository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateLabelInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
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
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"label": l}, ""), nil
	}
}

type DeleteLabelInput struct {
	LabelID string `json:"label_id"`
}
type DeleteLabelOutput struct {
	OK bool `json:"ok"`
}

func deleteLabel(labels label.Repository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DeleteLabelInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		id, err := parseUUID(in.LabelID, "label_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		if err := labels.Delete(ctx, id); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(DeleteLabelOutput{OK: true}, ""), nil
	}
}
