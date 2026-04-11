package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/epic"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func RegisterEpic(s *mcp.Server, epics *service.EpicService, _ interface{}) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_epics", Description: "List epics in a project"}, listEpics(epics))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_epic", Description: "Get an epic by ID including its description"}, getEpic(epics))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_epic", Description: "Create a new epic"}, createEpic(epics))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_epic", Description: "Update epic fields (partial)"}, updateEpic(epics))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_epic", Description: "Soft-delete an epic"}, deleteEpic(epics))
}

type ListEpicsInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
}

func listEpics(epics *service.EpicService) mcp.ToolHandlerFor[ListEpicsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListEpicsInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		es, total, err := epics.List(ctx, wsID, pID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"epics": es, "total": total}, nil
	}
}

type GetEpicInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	EpicID      string `json:"epic_id"`
}

func getEpic(epics *service.EpicService) mcp.ToolHandlerFor[GetEpicInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetEpicInput) (*mcp.CallToolResult, any, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		eID, err := parseUUID(in.EpicID, "epic_id")
		if err != nil {
			return nil, nil, err
		}
		e, err := epics.GetByID(ctx, wsID, pID, eID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"epic": e}, nil
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

func createEpic(epics *service.EpicService) mcp.ToolHandlerFor[CreateEpicInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateEpicInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		e := &epic.Epic{
			WorkspaceID: wsID,
			ProjectID:   pID,
			Name:        in.Name,
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
		created, err := epics.Create(ctx, e)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"epic": created}, nil
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

func updateEpic(epics *service.EpicService) mcp.ToolHandlerFor[UpdateEpicInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateEpicInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, nil, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, nil, err
		}
		eID, err := parseUUID(in.EpicID, "epic_id")
		if err != nil {
			return nil, nil, err
		}
		existing, err := epics.GetByID(ctx, wsID, pID, eID)
		if err != nil {
			return nil, nil, err
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
			return nil, nil, err
		}
		return nil, map[string]any{"epic": updated}, nil
	}
}

type DeleteEpicInput struct {
	WorkspaceID string `json:"workspace_id"`
	ProjectID   string `json:"project_id"`
	EpicID      string `json:"epic_id"`
}
type DeleteEpicOutput struct {
	OK bool `json:"ok"`
}

func deleteEpic(epics *service.EpicService) mcp.ToolHandlerFor[DeleteEpicInput, DeleteEpicOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteEpicInput) (*mcp.CallToolResult, DeleteEpicOutput, error) {
		wsID, err := parseUUID(in.WorkspaceID, "workspace_id")
		if err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		pID, err := parseUUID(in.ProjectID, "project_id")
		if err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		epicID, err := parseUUID(in.EpicID, "epic_id")
		if err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		if err := epics.DeleteByWorkspace(ctx, wsID, pID, epicID); err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		return nil, DeleteEpicOutput{OK: true}, nil
	}
}
