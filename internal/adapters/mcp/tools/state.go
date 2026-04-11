package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterState(s *mcp.Server, states state.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_states", Description: "Lists all workflow states for a project. Returns state ID, name, group (backlog/unstarted/started/completed/cancelled), and color. Use state IDs in tack_create_issue or tack_set_issue_state."}, listStates(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_state", Description: "Creates a workflow state in a project."}, createState(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_state", Description: "Updates a state name, color, or group. Only provided fields change."}, updateState(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_state", Description: "Deletes a workflow state. Issues in this state must be moved first or they become stateless."}, deleteState(states))
}

type ListStatesInput struct {
	ProjectID string `json:"project_id"`
}

func listStates(states state.Repository) mcp.ToolHandlerFor[ListStatesInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListStatesInput) (*mcp.CallToolResult, any, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		ss, err := states.List(ctx, pID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"states": ss}, ""), nil, nil
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
			return RecoverableError(err.Error()), nil, nil
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
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"state": s}, ""), nil, nil
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
			return RecoverableError(err.Error()), nil, nil
		}
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		s, err := states.GetByID(ctx, pID, sID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"state": updated}, ""), nil, nil
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
			return RecoverableError(err.Error()), DeleteStateOutput{}, nil
		}
		if err := states.Delete(ctx, sID); err != nil {
			return ClassifyError(ctx, err), DeleteStateOutput{}, nil
		}
		return Success(DeleteStateOutput{OK: true}, ""), DeleteStateOutput{}, nil
	}
}
