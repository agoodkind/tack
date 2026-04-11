package tools

import (
	"context"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterPrompts adds workflow prompt templates to the server.
// nodeTypes is the deduplicated set for the authenticated user's workspaces.
// Prompts are registered only when the required type keys exist in that set.
func RegisterPrompts(s *mcpserver.MCPServer, nodeTypes []*node.NodeType) {
	typeKeys := make(map[string]bool)
	for _, nt := range nodeTypes {
		if nt.TypeKey != "" {
			typeKeys[nt.TypeKey] = true
		}
	}

	// tack_onboard_workspace — always registered; universal starting point for the LLM.
	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_onboard_workspace",
		Description: "Getting-started guide: discovers all workspaces, projects, and node types before any operation. Always start here in a new conversation.",
	}, func(_ context.Context, _ mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		return &mcpmcp.GetPromptResult{
			Description: "Tack workspace onboarding",
			Messages: []mcpmcp.PromptMessage{{
				Role: "user",
				Content: &mcpmcp.TextContent{
					Text: "Start by calling tack_list_workspaces to see all available workspaces. " +
						"Then call tack_describe_workspace with a workspace slug to see its projects, " +
						"node types, states, and property definitions. " +
						"This gives you the full context needed before any create, update, or query operation. " +
						"Alternatively, read the tack://workspaces resource and tack://workspace/{slug} " +
						"resource for zero-tool-call context loading.",
				},
			}},
		}, nil
	})

	if !typeKeys["issue"] {
		return
	}

	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_triage",
		Description: "Issue triage workflow: review and prioritize issues in a project. Requires project_identifier.",
		Arguments: []mcpmcp.PromptArgument{
			{Name: "project_identifier", Description: "Project short identifier e.g. ENG", Required: true},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		projIdent := req.Params.Arguments["project_identifier"]
		body := "Triage issues in project " + projIdent + ":\n" +
			"1. Read tack://project/" + projIdent + " to confirm the workspace slug and available states.\n" +
			"2. Call tack_list_issues with project_identifier='" + projIdent + "' to see all open issues.\n" +
			"3. For each unstarted issue: set priority via tack_update_issue if unclear.\n" +
			"4. Assign unassigned issues using tack_assign_issue. Call tack_list_members for user IDs.\n" +
			"5. Move completed issues to a done state using tack_set_issue_state."
		return &mcpmcp.GetPromptResult{
			Description: "Issue triage for project " + projIdent,
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: &mcpmcp.TextContent{Text: body},
			}},
		}, nil
	})

	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_weekly_standup",
		Description: "Weekly standup: shows what the authenticated user is working on across all workspaces.",
		Arguments: []mcpmcp.PromptArgument{
			{Name: "project_identifier", Description: "Optional project short identifier e.g. ENG", Required: false},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		projIdent := req.Params.Arguments["project_identifier"]
		var body string
		if projIdent != "" {
			body = "Weekly standup for project " + projIdent + ":\n" +
				"1. Call tack_my_issues to see all issues assigned to you.\n" +
				"2. Filter by project_identifier='" + projIdent + "' if needed.\n" +
				"3. Group by state (started, unstarted, completed) and summarize progress.\n" +
				"4. Note any blockers or issues that need state updates."
		} else {
			body = "Weekly standup across all workspaces:\n" +
				"1. Call tack_my_issues to see all issues assigned to you.\n" +
				"2. Group by state (started, unstarted, completed) and by workspace/project.\n" +
				"3. Note any blockers or issues that need state updates."
		}
		return &mcpmcp.GetPromptResult{
			Description: "Weekly standup summary",
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: &mcpmcp.TextContent{Text: body},
			}},
		}, nil
	})

	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_create_bug_report",
		Description: "Creates a structured bug report issue with reproduction steps, expected/actual behavior, and environment fields.",
		Arguments: []mcpmcp.PromptArgument{
			{Name: "project_identifier", Description: "Project short identifier e.g. ENG", Required: true},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		projIdent := req.Params.Arguments["project_identifier"]
		body := "Create a bug report in project " + projIdent + ":\n" +
			"1. Read tack://project/" + projIdent + " to confirm the workspace slug and states.\n" +
			"2. Call tack_create_issue with:\n" +
			"   - project_identifier: '" + projIdent + "'\n" +
			"   - name: '[Bug] <short title>'\n" +
			"   - description (Markdown):\n\n" +
			"**Steps to reproduce:**\n1. \n2. \n\n" +
			"**Expected behavior:**\n\n" +
			"**Actual behavior:**\n\n" +
			"**Environment:**\n\n" +
			"3. Set priority to 'urgent' or 'high' as appropriate via tack_update_issue.\n" +
			"4. Assign to the responsible team member using tack_assign_issue (use tack_list_members for IDs)."
		return &mcpmcp.GetPromptResult{
			Description: "Bug report creation for project " + projIdent,
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: &mcpmcp.TextContent{Text: body},
			}},
		}, nil
	})

	if !typeKeys["cycle"] {
		return
	}

	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_sprint_setup",
		Description: "Sprint setup: creates a cycle and populates it with issues. Requires issue and cycle types.",
		Arguments: []mcpmcp.PromptArgument{
			{Name: "project_identifier", Description: "Project short identifier e.g. ENG", Required: true},
			{Name: "cycle_name", Description: "Name for the new cycle e.g. Sprint 3", Required: true},
			{Name: "start_date", Description: "Start date YYYY-MM-DD", Required: false},
			{Name: "end_date", Description: "End date YYYY-MM-DD", Required: false},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		args := req.Params.Arguments
		projIdent := args["project_identifier"]
		cycleName := args["cycle_name"]
		startDate := args["start_date"]
		endDate := args["end_date"]
		dateClause := ""
		if startDate != "" || endDate != "" {
			dateClause = " with start_date='" + startDate + "' end_date='" + endDate + "'"
		}
		body := "Sprint setup for project " + projIdent + ":\n" +
			"1. Read tack://project/" + projIdent + " to confirm workspace slug and states.\n" +
			"2. Call tack_create_cycle with project_identifier='" + projIdent + "', name='" + cycleName + "'" + dateClause + ".\n" +
			"3. Call tack_list_issues with project_identifier='" + projIdent + "' filtered to backlog/unstarted state.\n" +
			"4. Select issues for the sprint and call tack_add_to_cycle with their issue IDs.\n" +
			"5. Assign unassigned issues using tack_assign_issue."
		return &mcpmcp.GetPromptResult{
			Description: "Sprint setup: " + cycleName + " in project " + projIdent,
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: &mcpmcp.TextContent{Text: body},
			}},
		}, nil
	})
}
