package tools

import (
	"context"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/epic"
	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/agoodkind/tack/internal/domain/project"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterEpic(s *mcp.Server, epics epic.Repository, projects project.Repository) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_epics", Description: "List epics in a project"}, listEpics(epics))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_epic", Description: "Get an epic by ID including its description"}, getEpic(epics))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_epic", Description: "Create a new epic"}, createEpic(epics, projects))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_epic", Description: "Update epic fields (partial)"}, updateEpic(epics))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_epic", Description: "Soft-delete an epic"}, deleteEpic(epics))
}

type ListEpicsInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"` 
}
type ListEpicsOutput struct {
	Epics any `json:"epics"`
	Total int             `json:"total"`
}

func listEpics(epics epic.Repository) mcp.ToolHandlerFor[ListEpicsInput, ListEpicsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListEpicsInput) (*mcp.CallToolResult, ListEpicsOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, ListEpicsOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, ListEpicsOutput{}, err
		}
		es, total, err := epics.List(ctx, wsID, pID)
		if err != nil {
			return nil, ListEpicsOutput{}, err
		}
		return nil, ListEpicsOutput{Epics: es, Total: total}, nil
	}
}

type GetEpicInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"` 
	EpicID      string `json:"epic_id"` 
}
type GetEpicOutput struct {
	Epic any `json:"epic"`
}

func getEpic(epics epic.Repository) mcp.ToolHandlerFor[GetEpicInput, GetEpicOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetEpicInput) (*mcp.CallToolResult, GetEpicOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, GetEpicOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, GetEpicOutput{}, err
		}
		eID, err := parseUUID(in.EpicID, "epic_id")
		if err != nil {
			return nil, GetEpicOutput{}, err
		}
		e, err := epics.GetByID(ctx, wsID, pID, eID)
		if err != nil {
			return nil, GetEpicOutput{}, err
		}
		return nil, GetEpicOutput{Epic: e}, nil
	}
}

type CreateEpicInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	StateID     *string `json:"state_id,omitempty"`
}
type CreateEpicOutput struct {
	Epic any `json:"epic"`
}

func createEpic(epics epic.Repository, projects project.Repository) mcp.ToolHandlerFor[CreateEpicInput, CreateEpicOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateEpicInput) (*mcp.CallToolResult, CreateEpicOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, CreateEpicOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, CreateEpicOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, CreateEpicOutput{}, err
		}
		seq, err := projects.AllocateSequenceID(ctx, pID, "epic")
		if err != nil {
			return nil, CreateEpicOutput{}, fmt.Errorf("allocate sequence: %w", err)
		}
		e := &epic.Epic{
			WorkspaceID: wsID,
			ProjectID:   pID,
			Name:        in.Name,
			SequenceID:  seq,
			SortOrder:   65535,
		}
		if in.Description != nil {
			e.Description = *in.Description
		}
		if in.Priority != nil {
			e.Priority = issue.Priority(*in.Priority)
		}
		if in.StateID != nil {
			e.StateID = parseOptionalUUID(*in.StateID)
		}
		e.CreatedBy = userID
		e, err = epics.Create(ctx, e)
		if err != nil {
			return nil, CreateEpicOutput{}, err
		}
		return nil, CreateEpicOutput{Epic: e}, nil
	}
}

type UpdateEpicInput struct {
	WorkspaceID string  `json:"workspace_id"`
	ProjectID   string  `json:"project_id"`
	EpicID      string  `json:"epic_id"`
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	Priority    *string `json:"priority,omitempty"`
	StateID     *string `json:"state_id,omitempty"`
}
type UpdateEpicOutput struct {
	Epic any `json:"epic"`
}

func updateEpic(epics epic.Repository) mcp.ToolHandlerFor[UpdateEpicInput, UpdateEpicOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateEpicInput) (*mcp.CallToolResult, UpdateEpicOutput, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, UpdateEpicOutput{}, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, UpdateEpicOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, UpdateEpicOutput{}, err
		}
		eID, err := parseUUID(in.EpicID, "epic_id")
		if err != nil {
			return nil, UpdateEpicOutput{}, err
		}
		existing, err := epics.GetByID(ctx, wsID, pID, eID)
		if err != nil {
			return nil, UpdateEpicOutput{}, err
		}
		if in.Name != nil {
			existing.Name = *in.Name
		}
		if in.Description != nil {
			existing.Description = *in.Description
		}
		if in.Priority != nil {
			existing.Priority = issue.Priority(*in.Priority)
		}
		if in.StateID != nil {
			existing.StateID = parseOptionalUUID(*in.StateID)
		}
		existing.UpdatedBy = &userID
		updated, err := epics.Update(ctx, existing)
		if err != nil {
			return nil, UpdateEpicOutput{}, err
		}
		return nil, UpdateEpicOutput{Epic: updated}, nil
	}
}

type DeleteEpicInput struct {
	EpicID string `json:"epic_id"`
}
type DeleteEpicOutput struct {
	OK bool `json:"ok"`
}

func deleteEpic(epics epic.Repository) mcp.ToolHandlerFor[DeleteEpicInput, DeleteEpicOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteEpicInput) (*mcp.CallToolResult, DeleteEpicOutput, error) {
		id, err := parseUUID(in.EpicID, "epic_id")
		if err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		if err := epics.Delete(ctx, id); err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		return nil, DeleteEpicOutput{OK: true}, nil
	}
}
