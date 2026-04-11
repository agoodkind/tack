package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/state"
	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func RegisterState(s *mcpserver.MCPServer, states state.Repository) {
	s.AddTool(mcpmcp.Tool{Name: "tack_list_states", Description: "Lists all workflow states for a project. Returns state ID, name, group (backlog/unstarted/started/completed/cancelled), and color. Use state IDs in tack_create_issue or tack_set_issue_state.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"project_id": map[string]any{"type": "string", "description": "The project ID"}}, Required: []string{"project_id"}}}, listStates(states))
	s.AddTool(mcpmcp.Tool{Name: "tack_create_state", Description: "Creates a workflow state in a project.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"project_id": map[string]any{"type": "string", "description": "The project ID"}, "name": map[string]any{"type": "string", "description": "The state name"}, "group_name": map[string]any{"type": "string", "description": "The group name"}, "color": map[string]any{"type": "string", "description": "The state color"}}, Required: []string{"project_id", "name", "group_name"}}}, createState(states))
	s.AddTool(mcpmcp.Tool{Name: "tack_update_state", Description: "Updates a state name, color, or group. Only provided fields change.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"project_id": map[string]any{"type": "string", "description": "The project ID"}, "state_id": map[string]any{"type": "string", "description": "The state ID"}, "name": map[string]any{"type": "string", "description": "The state name"}, "group_name": map[string]any{"type": "string", "description": "The group name"}, "color": map[string]any{"type": "string", "description": "The state color"}}, Required: []string{"project_id", "state_id"}}}, updateState(states))
	s.AddTool(mcpmcp.Tool{Name: "tack_delete_state", Description: "Deletes a workflow state. Issues in this state must be moved first or they become stateless.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"state_id": map[string]any{"type": "string", "description": "The state ID"}}, Required: []string{"state_id"}}}, deleteState(states))
}

type ListStatesInput struct {
	ProjectID string `json:"project_id"`
}

func listStates(states state.Repository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListStatesInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		ss, err := states.List(ctx, pID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"states": ss}, ""), nil
	}
}

type CreateStateInput struct {
	ProjectID string  `json:"project_id"`
	Name      string  `json:"name"`
	GroupName string  `json:"group_name"`
	Color     *string `json:"color,omitempty"`
}

func createState(states state.Repository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateStateInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		color := "#cccccc"
		if in.Color != nil && *in.Color != "" {
			color = *in.Color
		}
		s, err := states.Create(ctx, &state.State{
			ProjectID: pID,
			Name:      in.Name,
			GroupName: state.GroupName(in.GroupName),
			Color:     color,
			SortOrder: 65535,
		})
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"state": s}, ""), nil
	}
}

type UpdateStateInput struct {
	ProjectID string  `json:"project_id"`
	StateID   string  `json:"state_id"`
	Name      *string `json:"name,omitempty"`
	GroupName *string `json:"group_name,omitempty"`
	Color     *string `json:"color,omitempty"`
}

func updateState(states state.Repository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in UpdateStateInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		s, err := states.GetByID(ctx, pID, sID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if in.Name != nil {
			s.Name = *in.Name
		}
		if in.GroupName != nil {
			s.GroupName = state.GroupName(*in.GroupName)
		}
		if in.Color != nil {
			s.Color = *in.Color
		}
		updated, err := states.Update(ctx, s)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"state": updated}, ""), nil
	}
}

type DeleteStateInput struct {
	StateID string `json:"state_id"`
}
type DeleteStateOutput struct {
	OK bool `json:"ok"`
}

func deleteState(states state.Repository) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DeleteStateInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		if err := states.Delete(ctx, sID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(DeleteStateOutput{OK: true}, ""), nil
	}
}
