package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/epic"
	"goodkind.io/tack/internal/domain/issue"
	"goodkind.io/tack/internal/service"
)

// registerEpicTools registers all epic-related MCP tools using slug-derived names.
func registerEpicTools(s *mcpserver.MCPServer, slug, pluralSlug string, epics *service.EpicService, r *Resolver) {
	s.AddTool(mcpmcp.Tool{Name: "tack_list_" + pluralSlug, Description: "Lists " + pluralSlug + " in a project. Returns identifier, name, state, priority, and description for each.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string"}, "project_identifier": map[string]any{"type": "string"}}, Required: []string{"workspace_slug", "project_identifier"}}}, listEpics(epics, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_get_" + slug, Description: "Fetches a single " + slug + " by identifier (e.g. ENG-42) with full description and all fields.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string"}, "identifier": map[string]any{"type": "string"}}, Required: []string{"workspace_slug", "identifier"}}}, getEpic(epics, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_create_" + slug, Description: "Creates a new " + slug + " in a project. Returns the created " + slug + " with its identifier.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string"}, "project_identifier": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "priority": map[string]any{"type": "string"}, "state_id": map[string]any{"type": "string"}}, Required: []string{"workspace_slug", "project_identifier", "name"}}}, createEpic(epics, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_update_" + slug, Description: "Updates fields on a " + slug + " by identifier. Only provided fields change; omitted fields are unchanged. Returns the updated " + slug + ".", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string"}, "identifier": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}, "priority": map[string]any{"type": "string"}, "state_id": map[string]any{"type": "string"}}, Required: []string{"workspace_slug", "identifier"}}}, updateEpic(epics, r))
	s.AddTool(mcpmcp.Tool{Name: "tack_delete_" + slug, Description: "Soft-deletes a " + slug + " by identifier. Deletion is permanent and irreversible. Returns ok: true.", InputSchema: mcpmcp.ToolInputSchema{Type: "object", Properties: map[string]any{"workspace_slug": map[string]any{"type": "string"}, "identifier": map[string]any{"type": "string"}}, Required: []string{"workspace_slug", "identifier"}}}, deleteEpic(epics, r))
}

type ListEpicsInput struct {
	WorkspaceSlug     string `json:"workspace_slug"`
	ProjectIdentifier string `json:"project_identifier"`
}

func listEpics(epics *service.EpicService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in ListEpicsInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		es, total, err := epics.List(ctx, ws.ID, proj.ID)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"epics": es, "total": total}, ""), nil
	}
}

type GetEpicInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	Identifier    string `json:"identifier"`
}

func getEpic(epics *service.EpicService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in GetEpicInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		e, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"epic": e}, ""), nil
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

func createEpic(epics *service.EpicService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in CreateEpicInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, in.ProjectIdentifier)
		if err != nil {
			return ClassifyError(ctx, err), nil
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
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"epic": created}, "Created. Use tack_update_epic to set more fields."), nil
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

func updateEpic(epics *service.EpicService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in UpdateEpicInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		userID, err := mustUser(ctx)
		if err != nil {
			return UnexpectedError(ctx, err), nil
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		existing, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return ClassifyError(ctx, err), nil
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
			return ClassifyError(ctx, err), nil
		}
		return Success(map[string]any{"epic": updated}, ""), nil
	}
}

type DeleteEpicInput struct {
	WorkspaceSlug string `json:"workspace_slug"`
	Identifier    string `json:"identifier"`
}
type DeleteEpicOutput struct {
	OK bool `json:"ok"`
}

func deleteEpic(epics *service.EpicService, r *Resolver) mcpserver.ToolHandlerFunc {
	return func(ctx context.Context, req mcpmcp.CallToolRequest) (*mcpmcp.CallToolResult, error) {
		var in DeleteEpicInput
		if err := req.BindArguments(&in); err != nil {
			return RecoverableError(err.Error()), nil
		}
		projIdent, seq, err := ParseNodeIdentifier(in.Identifier)
		if err != nil {
			return RecoverableError(err.Error()), nil
		}
		ws, proj, err := r.Project(ctx, in.WorkspaceSlug, projIdent)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		existing, err := epics.GetBySequence(ctx, ws.ID, proj.ID, seq)
		if err != nil {
			return ClassifyError(ctx, err), nil
		}
		if err := epics.DeleteByWorkspace(ctx, ws.ID, proj.ID, existing.ID); err != nil {
			return ClassifyError(ctx, err), nil
		}
		return Success(DeleteEpicOutput{OK: true}, "Deletion is permanent and irreversible."), nil
	}
}
