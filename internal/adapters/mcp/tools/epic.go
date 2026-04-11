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
	mcp.AddTool(s, &mcp.Tool{Name: "tack_list_" + pluralSlug, Description: "Lists " + pluralSlug + " in a project. Returns identifier, name, state, priority, and description for each."}, listEpics(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_get_" + slug, Description: "Fetches a single " + slug + " by identifier (e.g. ENG-42) with full description and all fields."}, getEpic(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_create_" + slug, Description: "Creates a new " + slug + " in a project. Returns the created " + slug + " with its identifier."}, createEpic(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_update_" + slug, Description: "Updates fields on a " + slug + " by identifier. Only provided fields change; omitted fields are unchanged. Returns the updated " + slug + "."}, updateEpic(epics, r))
	mcp.AddTool(s, &mcp.Tool{Name: "tack_delete_" + slug, Description: "Soft-deletes a " + slug + " by identifier. Deletion is permanent and irreversible. Returns ok: true."}, deleteEpic(epics, r))
}

type ListEpicsInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func listEpics(epics *service.EpicService, r *Resolver) mcp.ToolHandlerFor[ListEpicsInput, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListEpicsInput) (*mcp.CallToolResult, any, error) {
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		es, total, err := epics.List(ctx, ws.ID, proj.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"epics": es, "total": total}, ""), nil, nil
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
			return RecoverableError(err.Error()), nil, nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		e, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"epic": e}, ""), nil, nil
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
			return UnexpectedError(ctx, err), nil, nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"epic": created}, "Created. Use tack_update_epic to set more fields."), nil, nil
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
			return UnexpectedError(ctx, err), nil, nil
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return RecoverableError(err.Error()), nil, nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
		}
		existing, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return ClassifyError(ctx, err), nil, nil
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
			return ClassifyError(ctx, err), nil, nil
		}
		return Success(map[string]any{"epic": updated}, ""), nil, nil
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
			return RecoverableError(err.Error()), DeleteEpicOutput{}, nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return ClassifyError(ctx, err), DeleteEpicOutput{}, nil
		}
		existing, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return ClassifyError(ctx, err), DeleteEpicOutput{}, nil
		}
		if err := epics.DeleteByWorkspace(ctx, ws.ID, proj.ID, existing.ID); err != nil {
			return ClassifyError(ctx, err), DeleteEpicOutput{}, nil
		}
		return Success(DeleteEpicOutput{OK: true}, "Deletion is permanent and irreversible."), DeleteEpicOutput{}, nil
	}
}
