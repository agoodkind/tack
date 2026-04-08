package tools

import (
	"context"
	"encoding/json"

	"github.com/agoodkind/tack/internal/domain/state"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterState(s *mcp.Server, states state.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_states", Description: "List all workflow states for a project"}, listStates(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_state", Description: "Create a new workflow state in a project"}, createState(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_state", Description: "Update a state's name, color, or group (partial)"}, updateState(states))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_state", Description: "Delete a workflow state"}, deleteState(states))
}

type ListStatesInput struct {
	ProjectID string `json:"project_id" jsonschema:"required"`
}
type ListStatesOutput struct {
	States json.RawMessage `json:"states"`
}

func listStates(states state.Repository) mcp.ToolHandlerFor[ListStatesInput, ListStatesOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListStatesInput) (*mcp.CallToolResult, ListStatesOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, ListStatesOutput{}, err
		}
		ss, err := states.List(ctx, pID)
		if err != nil {
			return nil, ListStatesOutput{}, err
		}
		b, _ := json.Marshal(ss)
		return nil, ListStatesOutput{States: b}, nil
	}
}

type CreateStateInput struct {
	ProjectID string `json:"project_id" jsonschema:"required"`
	Name      string `json:"name"       jsonschema:"required"`
	GroupName string `json:"group_name" jsonschema:"required"`
	Color     string `json:"color"     `
}
type CreateStateOutput struct {
	State json.RawMessage `json:"state"`
}

func createState(states state.Repository) mcp.ToolHandlerFor[CreateStateInput, CreateStateOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateStateInput) (*mcp.CallToolResult, CreateStateOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, CreateStateOutput{}, err
		}
		color := in.Color
		if color == "" {
			color = "#cccccc"
		}
		s, err := states.Create(ctx, &state.State{
			ProjectID: pID,
			Name:      in.Name,
			GroupName: state.GroupName(in.GroupName),
			Color:     color,
			SortOrder: 65535,
		})
		if err != nil {
			return nil, CreateStateOutput{}, err
		}
		b, _ := json.Marshal(s)
		return nil, CreateStateOutput{State: b}, nil
	}
}

type UpdateStateInput struct {
	ProjectID string `json:"project_id" jsonschema:"required"`
	StateID   string `json:"state_id"   jsonschema:"required"`
	Name      string `json:"name"      `
	GroupName string `json:"group_name"`
	Color     string `json:"color"     `
}
type UpdateStateOutput struct {
	State json.RawMessage `json:"state"`
}

func updateState(states state.Repository) mcp.ToolHandlerFor[UpdateStateInput, UpdateStateOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateStateInput) (*mcp.CallToolResult, UpdateStateOutput, error) {
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, UpdateStateOutput{}, err
		}
		sID, err := parseUUID(in.StateID, "state_id")
		if err != nil {
			return nil, UpdateStateOutput{}, err
		}
		s, err := states.GetByID(ctx, pID, sID)
		if err != nil {
			return nil, UpdateStateOutput{}, err
		}
		if in.Name != "" {
			s.Name = in.Name
		}
		if in.GroupName != "" {
			s.GroupName = state.GroupName(in.GroupName)
		}
		if in.Color != "" {
			s.Color = in.Color
		}
		updated, err := states.Update(ctx, s)
		if err != nil {
			return nil, UpdateStateOutput{}, err
		}
		b, _ := json.Marshal(updated)
		return nil, UpdateStateOutput{State: b}, nil
	}
}

type DeleteStateInput struct {
	StateID string `json:"state_id" jsonschema:"required"`
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
