package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterState(s *mcp.Server, states state.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_states", Description: "List all workflow states for a project"}, listStates(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_state", Description: "Create a new workflow state in a project"}, createState(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_state", Description: "Update a state's name, color, or group (partial)"}, updateState(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_state", Description: "Delete a workflow state"}, deleteState(states))
}

type ListStatesInput struct {
	ProjectID string `json:"project_id"`
}

func listStates(states state.Repository) mcp.ToolHandlerFor[ListStatesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListStatesInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		ss, err := states.List(ctx, pID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"states": ss}, nil
	}
}

type CreateStateInput struct {
	ProjectID string  `json:"project_id"`
	Name      string  `json:"name"`
	GroupName string  `json:"group_name"`
	Color     *string `json:"color,omitempty"`
}

func createState(states state.Repository) mcp.ToolHandlerFor[CreateStateInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateStateInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
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
			return nil, nil, err
		}
		return nil, map[string]any{"state": s}, nil
	}
}

type UpdateStateInput struct {
	ProjectID string  `json:"project_id"`
	StateID   string  `json:"state_id"`
	Name      *string `json:"name,omitempty"`
	GroupName *string `json:"group_name,omitempty"`
	Color     *string `json:"color,omitempty"`
}

func updateState(states state.Repository) mcp.ToolHandlerFor[UpdateStateInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateStateInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return nil, nil, err
		}
		s, err := states.GetByID(ctx, pID, sID)
		if err != nil {
			return nil, nil, err
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
			return nil, nil, err
		}
		return nil, map[string]any{"state": updated}, nil
	}
}

type DeleteStateInput struct {
	StateID string `json:"state_id"`
}
type DeleteStateOutput struct {
	OK bool `json:"ok"`
}

func deleteState(states state.Repository) mcp.ToolHandlerFor[DeleteStateInput, DeleteStateOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteStateInput) (*mcp.CallToolResult, DeleteStateOutput, error) {
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return nil, DeleteStateOutput{}, err
		}
		if err := states.Delete(ctx, sID); err != nil {
			return nil, DeleteStateOutput{}, err
		}
		return nil, DeleteStateOutput{OK: true}, nil
	}
}
