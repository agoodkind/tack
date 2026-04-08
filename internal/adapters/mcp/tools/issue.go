package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/agoodkind/tack/internal/domain/issue"
	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- list ---

type ListWorkItemsInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	ProjectID   string `json:"project_id"   jsonschema:"required,description=Project UUID"`
	StateID     string `json:"state_id"     jsonschema:"description=Filter by state UUID"`
	Priority    string `json:"priority"     jsonschema:"description=Filter by priority: none|low|medium|high|urgent"`
}

type ListWorkItemsOutput struct {
	Items json.RawMessage `json:"items" jsonschema:"description=List of work items"`
	Total int             `json:"total" jsonschema:"description=Total count"`
}

func ListWorkItems(svc issue.Service) mcp.ToolHandlerFor[ListWorkItemsInput, ListWorkItemsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ListWorkItemsInput) (*mcp.CallToolResult, ListWorkItemsOutput, error) {
		wsID, err := uuid.Parse(in.WorkspaceID)
		if err != nil {
			return nil, ListWorkItemsOutput{}, fmt.Errorf("invalid workspace_id: %w", err)
		}
		projectID, err := uuid.Parse(in.ProjectID)
		if err != nil {
			return nil, ListWorkItemsOutput{}, fmt.Errorf("invalid project_id: %w", err)
		}
		filter := issue.ListFilter{WorkspaceID: wsID, ProjectID: &projectID}
		if in.StateID != "" {
			id, err := uuid.Parse(in.StateID)
			if err != nil {
				return nil, ListWorkItemsOutput{}, fmt.Errorf("invalid state_id: %w", err)
			}
			filter.StateIDs = []uuid.UUID{id}
		}
		if in.Priority != "" {
			filter.Priorities = []issue.Priority{issue.Priority(in.Priority)}
		}
		items, total, err := svc.List(ctx, filter)
		if err != nil {
			return nil, ListWorkItemsOutput{}, err
		}
		b, _ := json.Marshal(items)
		return nil, ListWorkItemsOutput{Items: b, Total: total}, nil
	}
}

// --- create ---

type CreateWorkItemInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	ProjectID   string `json:"project_id"   jsonschema:"required,description=Project UUID"`
	Name        string `json:"name"         jsonschema:"required,description=Issue title"`
	Description string `json:"description"  jsonschema:"description=Issue description"`
	Priority    string `json:"priority"     jsonschema:"description=none|low|medium|high|urgent"`
	StateID     string `json:"state_id"     jsonschema:"description=State UUID"`
}

type CreateWorkItemOutput struct {
	Item json.RawMessage `json:"item" jsonschema:"description=Created work item"`
}

func CreateWorkItem(svc issue.Service) mcp.ToolHandlerFor[CreateWorkItemInput, CreateWorkItemOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in CreateWorkItemInput) (*mcp.CallToolResult, CreateWorkItemOutput, error) {
		wsID, err := uuid.Parse(in.WorkspaceID)
		if err != nil {
			return nil, CreateWorkItemOutput{}, fmt.Errorf("invalid workspace_id: %w", err)
		}
		projectID, err := uuid.Parse(in.ProjectID)
		if err != nil {
			return nil, CreateWorkItemOutput{}, fmt.Errorf("invalid project_id: %w", err)
		}
		i := &issue.Issue{
			WorkspaceID: wsID,
			ProjectID:   projectID,
			Name:        in.Name,
			Description: in.Description,
			Priority:    issue.Priority(in.Priority),
		}
		if in.StateID != "" {
			id, err := uuid.Parse(in.StateID)
			if err != nil {
				return nil, CreateWorkItemOutput{}, fmt.Errorf("invalid state_id: %w", err)
			}
			i.StateID = &id
		}
		created, err := svc.Create(ctx, i)
		if err != nil {
			return nil, CreateWorkItemOutput{}, err
		}
		b, _ := json.Marshal(created)
		return nil, CreateWorkItemOutput{Item: b}, nil
	}
}

// --- get ---

type GetWorkItemInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	ProjectID   string `json:"project_id"   jsonschema:"required,description=Project UUID"`
	IssueID     string `json:"issue_id"     jsonschema:"required,description=Issue UUID"`
}

type GetWorkItemOutput struct {
	Item json.RawMessage `json:"item" jsonschema:"description=Work item"`
}

func GetWorkItem(svc issue.Service) mcp.ToolHandlerFor[GetWorkItemInput, GetWorkItemOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in GetWorkItemInput) (*mcp.CallToolResult, GetWorkItemOutput, error) {
		wsID, err := uuid.Parse(in.WorkspaceID)
		if err != nil {
			return nil, GetWorkItemOutput{}, fmt.Errorf("invalid workspace_id: %w", err)
		}
		projectID, err := uuid.Parse(in.ProjectID)
		if err != nil {
			return nil, GetWorkItemOutput{}, fmt.Errorf("invalid project_id: %w", err)
		}
		issueID, err := uuid.Parse(in.IssueID)
		if err != nil {
			return nil, GetWorkItemOutput{}, fmt.Errorf("invalid issue_id: %w", err)
		}
		item, err := svc.Get(ctx, wsID, projectID, issueID)
		if err != nil {
			return nil, GetWorkItemOutput{}, err
		}
		b, _ := json.Marshal(item)
		return nil, GetWorkItemOutput{Item: b}, nil
	}
}

// --- update ---

type UpdateWorkItemInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	ProjectID   string `json:"project_id"   jsonschema:"required,description=Project UUID"`
	IssueID     string `json:"issue_id"     jsonschema:"required,description=Issue UUID"`
	Name        string `json:"name"         jsonschema:"description=New title"`
	Priority    string `json:"priority"     jsonschema:"description=none|low|medium|high|urgent"`
	StateID     string `json:"state_id"     jsonschema:"description=New state UUID"`
}

type UpdateWorkItemOutput struct {
	Item json.RawMessage `json:"item" jsonschema:"description=Updated work item"`
}

func UpdateWorkItem(svc issue.Service) mcp.ToolHandlerFor[UpdateWorkItemInput, UpdateWorkItemOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in UpdateWorkItemInput) (*mcp.CallToolResult, UpdateWorkItemOutput, error) {
		wsID, err := uuid.Parse(in.WorkspaceID)
		if err != nil {
			return nil, UpdateWorkItemOutput{}, fmt.Errorf("invalid workspace_id: %w", err)
		}
		projectID, err := uuid.Parse(in.ProjectID)
		if err != nil {
			return nil, UpdateWorkItemOutput{}, fmt.Errorf("invalid project_id: %w", err)
		}
		issueID, err := uuid.Parse(in.IssueID)
		if err != nil {
			return nil, UpdateWorkItemOutput{}, fmt.Errorf("invalid issue_id: %w", err)
		}
		existing, err := svc.Get(ctx, wsID, projectID, issueID)
		if err != nil {
			return nil, UpdateWorkItemOutput{}, err
		}
		if in.Name != "" {
			existing.Name = in.Name
		}
		if in.Priority != "" {
			existing.Priority = issue.Priority(in.Priority)
		}
		if in.StateID != "" {
			id, err := uuid.Parse(in.StateID)
			if err != nil {
				return nil, UpdateWorkItemOutput{}, fmt.Errorf("invalid state_id: %w", err)
			}
			existing.StateID = &id
		}
		updated, err := svc.Update(ctx, existing)
		if err != nil {
			return nil, UpdateWorkItemOutput{}, err
		}
		b, _ := json.Marshal(updated)
		return nil, UpdateWorkItemOutput{Item: b}, nil
	}
}

// --- delete ---

type DeleteWorkItemInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	ProjectID   string `json:"project_id"   jsonschema:"required,description=Project UUID"`
	IssueID     string `json:"issue_id"     jsonschema:"required,description=Issue UUID"`
}

type DeleteWorkItemOutput struct {
	OK bool `json:"ok" jsonschema:"description=True if deleted"`
}

func DeleteWorkItem(svc issue.Service) mcp.ToolHandlerFor[DeleteWorkItemInput, DeleteWorkItemOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in DeleteWorkItemInput) (*mcp.CallToolResult, DeleteWorkItemOutput, error) {
		wsID, err := uuid.Parse(in.WorkspaceID)
		if err != nil {
			return nil, DeleteWorkItemOutput{}, fmt.Errorf("invalid workspace_id: %w", err)
		}
		projectID, err := uuid.Parse(in.ProjectID)
		if err != nil {
			return nil, DeleteWorkItemOutput{}, fmt.Errorf("invalid project_id: %w", err)
		}
		issueID, err := uuid.Parse(in.IssueID)
		if err != nil {
			return nil, DeleteWorkItemOutput{}, fmt.Errorf("invalid issue_id: %w", err)
		}
		if err := svc.Delete(ctx, wsID, projectID, issueID); err != nil {
			return nil, DeleteWorkItemOutput{}, err
		}
		return nil, DeleteWorkItemOutput{OK: true}, nil
	}
}

// --- search ---

type SearchWorkItemsInput struct {
	WorkspaceID string `json:"workspace_id" jsonschema:"required,description=Workspace UUID"`
	Query       string `json:"query"        jsonschema:"required,description=Search query"`
	ProjectID   string `json:"project_id"   jsonschema:"description=Scope to a specific project UUID"`
}

type SearchWorkItemsOutput struct {
	Items json.RawMessage `json:"items" jsonschema:"description=Matching work items"`
	Total int             `json:"total" jsonschema:"description=Total count"`
}

func SearchWorkItems(svc issue.Service) mcp.ToolHandlerFor[SearchWorkItemsInput, SearchWorkItemsOutput] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SearchWorkItemsInput) (*mcp.CallToolResult, SearchWorkItemsOutput, error) {
		wsID, err := uuid.Parse(in.WorkspaceID)
		if err != nil {
			return nil, SearchWorkItemsOutput{}, fmt.Errorf("invalid workspace_id: %w", err)
		}
		filter := issue.ListFilter{}
		if in.ProjectID != "" {
			id, err := uuid.Parse(in.ProjectID)
			if err != nil {
				return nil, SearchWorkItemsOutput{}, fmt.Errorf("invalid project_id: %w", err)
			}
			filter.ProjectID = &id
		}
		items, total, err := svc.Search(ctx, wsID, in.Query, filter)
		if err != nil {
			return nil, SearchWorkItemsOutput{}, err
		}
		b, _ := json.Marshal(items)
		return nil, SearchWorkItemsOutput{Items: b, Total: total}, nil
	}
}
