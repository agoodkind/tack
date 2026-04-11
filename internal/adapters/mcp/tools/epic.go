package tools

import (
	"context"

	"goodkind.io/tack/internal/domain/epic"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/service"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// registerEpicTools registers all epic-related MCP tools using slug-derived names.
func registerEpicTools(s *mcp.Server, slug, pluralSlug string, epics *service.EpicService, r *Resolver) {
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_" + pluralSlug, Description: "List " + pluralSlug + " in a project"}, listEpics(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_" + slug, Description: "Get a " + slug + " by ID including its description"}, getEpic(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_" + slug, Description: "Create a new " + slug}, createEpic(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_" + slug, Description: "Update " + slug + " fields (partial)"}, updateEpic(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_" + slug, Description: "Soft-delete a " + slug}, deleteEpic(epics, r))
}

type ListEpicsInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func listEpics(epics *service.EpicService, r *Resolver) mcp.ToolHandlerFor[ListEpicsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListEpicsInput) (*mcp.CallToolResult, any, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		es, total, err := epics.List(ctx, ws.ID, proj.ID)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"epics": es, "total": total}, nil
	}
}

type GetEpicInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	Identifier    string `json:"identifier"`
}

func getEpic(epics *service.EpicService, r *Resolver) mcp.ToolHandlerFor[GetEpicInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetEpicInput) (*mcp.CallToolResult, any, error) {
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, nil, err
		}
		e, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return nil, nil, err
		}
		return nil, map[string]any{"epic": e}, nil
	}
}

type CreateEpicInput struct {
	WorkspaceSlug     string  `json:"workspace_slug"`
	ProjectIdentifier string  `json:"project_identifier"`
	Name              string  `json:"name"`
	Description       *string `json:"description,omitempty"`
	Priority          *string `json:"priority,omitempty"`
	StateID           *string `json:"state_id,omitempty"`
}

func createEpic(epics *service.EpicService, r *Resolver) mcp.ToolHandlerFor[CreateEpicInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateEpicInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return nil, nil, err
		}
		e := &epic.Epic{
			WorkspaceID: ws.ID,
			ProjectID:   proj.ID,
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
	WorkspaceSlug string  `json:"workspace_slug"`
	Identifier    string  `json:"identifier"`
	Name          *string `json:"name,omitempty"`
	Description   *string `json:"description,omitempty"`
	Priority      *string `json:"priority,omitempty"`
	StateID       *string `json:"state_id,omitempty"`
}

func updateEpic(epics *service.EpicService, r *Resolver) mcp.ToolHandlerFor[UpdateEpicInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateEpicInput) (*mcp.CallToolResult, any, error) {
		userID, err := mustUser(ctx)
		if err != nil {
			return nil, nil, err
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, nil, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, nil, err
		}
		existing, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
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
	WorkspaceSlug string `json:"workspace_slug"`
	Identifier    string `json:"identifier"`
}
type DeleteEpicOutput struct {
	OK bool `json:"ok"`
}

func deleteEpic(epics *service.EpicService, r *Resolver) mcp.ToolHandlerFor[DeleteEpicInput, DeleteEpicOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteEpicInput) (*mcp.CallToolResult, DeleteEpicOutput, error) {
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		existing, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		if err := epics.DeleteByWorkspace(ctx, ws.ID, proj.ID, existing.ID); err != nil {
			return nil, DeleteEpicOutput{}, err
		}
		return nil, DeleteEpicOutput{OK: true}, nil
	}
}
