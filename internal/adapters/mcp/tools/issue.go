package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
)

func ListWorkItems(svc issue.Service) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectID, err := uuidArg(req, "project_id")
		if err != nil {
			return toolErr(err), nil
		}

		filter := issue.ListFilter{
			ProjectID: &projectID,
			PerPage:   100,
		}
		if raw := stringArg(req, "state_id"); raw != "" {
			id, _ := uuid.Parse(raw)
			filter.StateIDs = []uuid.UUID{id}
		}
		if p := stringArg(req, "priority"); p != "" {
			filter.Priorities = []issue.Priority{issue.Priority(p)}
		}
		if raw := stringArg(req, "assignee_id"); raw != "" {
			id, _ := uuid.Parse(raw)
			filter.AssigneeIDs = []uuid.UUID{id}
		}

		issues, total, err := svc.List(ctx, filter)
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(map[string]any{"results": issues, "total_count": total}), nil
	}
}

func CreateWorkItem(svc issue.Service) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectID, err := uuidArg(req, "project_id")
		if err != nil {
			return toolErr(err), nil
		}

		i := &issue.Issue{
			ProjectID:       projectID,
			Name:            stringArg(req, "name"),
			DescriptionHTML: stringArg(req, "description"),
			Priority:        issue.Priority(stringArgDefault(req, "priority", "none")),
		}
		if raw := stringArg(req, "state_id"); raw != "" {
			id, _ := uuid.Parse(raw)
			i.StateID = &id
		}

		created, err := svc.Create(ctx, i)
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(created), nil
	}
}

func GetWorkItem(svc issue.Service) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectID, err := uuidArg(req, "project_id")
		if err != nil {
			return toolErr(err), nil
		}
		issueID, err := uuidArg(req, "issue_id")
		if err != nil {
			return toolErr(err), nil
		}

		i, err := svc.Get(ctx, uuid.Nil, projectID, issueID)
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(i), nil
	}
}

func UpdateWorkItem(svc issue.Service) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectID, err := uuidArg(req, "project_id")
		if err != nil {
			return toolErr(err), nil
		}
		issueID, err := uuidArg(req, "issue_id")
		if err != nil {
			return toolErr(err), nil
		}

		existing, err := svc.Get(ctx, uuid.Nil, projectID, issueID)
		if err != nil {
			return toolErr(err), nil
		}

		if v := stringArg(req, "name"); v != "" {
			existing.Name = v
		}
		if v := stringArg(req, "priority"); v != "" {
			existing.Priority = issue.Priority(v)
		}
		if v := stringArg(req, "state_id"); v != "" {
			id, _ := uuid.Parse(v)
			existing.StateID = &id
		}

		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(updated), nil
	}
}

func DeleteWorkItem(svc issue.Service) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		projectID, err := uuidArg(req, "project_id")
		if err != nil {
			return toolErr(err), nil
		}
		issueID, err := uuidArg(req, "issue_id")
		if err != nil {
			return toolErr(err), nil
		}

		if err := svc.Delete(ctx, uuid.Nil, projectID, issueID); err != nil {
			return toolErr(err), nil
		}
		return toolJSON(map[string]string{"status": "deleted"}), nil
	}
}

func SearchWorkItems(svc issue.Service) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		wsSlug := stringArg(req, "workspace_slug")
		if wsSlug == "" {
			return toolErr(fmt.Errorf("workspace_slug required")), nil
		}
		q := stringArg(req, "query")

		filter := issue.ListFilter{PerPage: 50}
		if raw := stringArg(req, "project_id"); raw != "" {
			id, _ := uuid.Parse(raw)
			filter.ProjectID = &id
		}

		issues, total, err := svc.Search(ctx, uuid.Nil, q, filter)
		if err != nil {
			return toolErr(err), nil
		}
		return toolJSON(map[string]any{"results": issues, "total_count": total}), nil
	}
}

// helpers

func stringArg(req mcp.CallToolRequest, key string) string {
	return req.GetString(key, "")
}

func stringArgDefault(req mcp.CallToolRequest, key, def string) string {
	return req.GetString(key, def)
}

func uuidArg(req mcp.CallToolRequest, key string) (uuid.UUID, error) {
	raw, err := req.RequireString(key)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s is required", key)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%s: invalid UUID: %w", key, err)
	}
	return id, nil
}

func toolJSON(v any) *mcp.CallToolResult {
	b, _ := json.Marshal(v)
	return mcp.NewToolResultText(string(b))
}

func toolErr(err error) *mcp.CallToolResult {
	return mcp.NewToolResultError(err.Error())
}
