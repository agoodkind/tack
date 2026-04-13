package tools

import (
	"context"
	"fmt"
	"strings"

	mcpmcp "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"goodkind.io/tack/internal/domain/node"
)

// RegisterPrompts adds workflow prompt templates to the server.
// Prompts are gated on Features, not type names. Tool names are derived from
// type slugs so they adapt if types are renamed or custom types are added.
func RegisterPrompts(s *mcpserver.MCPServer, nodeTypes []*node.NodeType) {
	// Discover types by features for prompt gating and tool name generation.
	var workflowType, containerWithDatesType *node.NodeType
	var entryPointPlural string
	for _, nt := range nodeTypes {
		if nt.Features.Has(node.FeatureHasWorkflowStates) && nt.Features.Has(node.FeatureHasSequenceID) && workflowType == nil {
			workflowType = nt
		}
		if nt.Features.Has(node.FeatureIsContainer) && nt.Features.Has(node.FeatureHasDueDates) && containerWithDatesType == nil {
			containerWithDatesType = nt
		}
		if nt.Features.Has(node.FeatureIsEntryPoint) {
			entryPointPlural = strings.ToLower(nt.PluralSlug)
			if entryPointPlural == "" {
				entryPointPlural = strings.ToLower(nt.Slug) + "s"
			}
		}
	}

	// tack_onboard -- always registered; universal starting point for the LLM.
	listToolName := fmt.Sprintf("tack_list_%s", entryPointPlural)
	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_onboard",
		Description: "Getting-started guide: discovers all entry points, containers, and node types before any operation. Always start here in a new conversation.",
	}, func(_ context.Context, _ mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		return &mcpmcp.GetPromptResult{
			Description: "Tack onboarding",
			Messages: []mcpmcp.PromptMessage{{
				Role: "user",
				Content: mcpmcp.TextContent{Type: "text",
					Text: fmt.Sprintf(
						"Start by calling %s to see all available entry points. "+
							"Then call tack_describe_{slug} with the slug to see containers, "+
							"node types, and property definitions. "+
							"This gives you the full context needed before any create, update, or query operation.",
						listToolName),
				},
			}},
		}, nil
	})

	if workflowType == nil {
		return
	}

	wfSlug := strings.ToLower(workflowType.Slug)
	wfPlural := strings.ToLower(workflowType.PluralSlug)
	if wfPlural == "" {
		wfPlural = wfSlug + "s"
	}
	listWF := fmt.Sprintf("tack_list_%s", wfPlural)
	updateWF := fmt.Sprintf("tack_update_%s", wfSlug)

	// tack_triage -- review and prioritize workflow items.
	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_triage",
		Description: fmt.Sprintf("Triage workflow: review and prioritize %s in a container.", wfPlural),
		Arguments: []mcpmcp.PromptArgument{
			{Name: "container_identifier", Description: "Container short identifier e.g. ENG", Required: true},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		ident := req.Params.Arguments["container_identifier"]
		body := fmt.Sprintf("Triage %s in container %s:\n", wfPlural, ident) +
			fmt.Sprintf("1. Read tack://project/%s to confirm the entry point slug and available states.\n", ident) +
			fmt.Sprintf("2. Call %s to see all open %s.\n", listWF, wfPlural) +
			fmt.Sprintf("3. For each unstarted item: set priority via %s if unclear.\n", updateWF) +
			"4. Assign unassigned items using " + updateWF + ". Call tack_list_members for user IDs.\n" +
			"5. Move completed items to a done state using " + updateWF + "."
		return &mcpmcp.GetPromptResult{
			Description: fmt.Sprintf("Triage for %s", ident),
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: mcpmcp.TextContent{Type: "text", Text: body},
			}},
		}, nil
	})

	// tack_weekly_standup
	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_weekly_standup",
		Description: "Weekly standup: shows what the authenticated user is working on.",
		Arguments: []mcpmcp.PromptArgument{
			{Name: "container_identifier", Description: "Optional container short identifier e.g. ENG", Required: false},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		ident := req.Params.Arguments["container_identifier"]
		var body string
		if ident != "" {
			body = fmt.Sprintf("Weekly standup for %s:\n", ident) +
				"1. Call tack_my_work to see all items assigned to you.\n" +
				fmt.Sprintf("2. Filter by container %s if needed.\n", ident) +
				"3. Group by state and summarize progress.\n" +
				"4. Note any blockers or items that need state updates."
		} else {
			body = "Weekly standup across all entry points:\n" +
				"1. Call tack_my_work to see all items assigned to you.\n" +
				"2. Group by state and by entry point/container.\n" +
				"3. Note any blockers or items that need state updates."
		}
		return &mcpmcp.GetPromptResult{
			Description: "Weekly standup summary",
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: mcpmcp.TextContent{Type: "text", Text: body},
			}},
		}, nil
	})

	// tack_create_bug_report
	createWF := fmt.Sprintf("tack_create_%s", wfSlug)
	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_create_bug_report",
		Description: fmt.Sprintf("Creates a structured bug report %s with reproduction steps.", wfSlug),
		Arguments: []mcpmcp.PromptArgument{
			{Name: "container_identifier", Description: "Container short identifier e.g. ENG", Required: true},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		ident := req.Params.Arguments["container_identifier"]
		body := fmt.Sprintf("Create a bug report in %s:\n", ident) +
			fmt.Sprintf("1. Read tack://project/%s to confirm states.\n", ident) +
			fmt.Sprintf("2. Call %s with:\n", createWF) +
			"   - name: '[Bug] <short title>'\n" +
			"   - description (Markdown):\n\n" +
			"**Steps to reproduce:**\n1. \n2. \n\n" +
			"**Expected behavior:**\n\n" +
			"**Actual behavior:**\n\n" +
			"**Environment:**\n\n" +
			fmt.Sprintf("3. Set priority via %s.\n", updateWF) +
			"4. Assign via " + updateWF + " (use tack_list_members for IDs)."
		return &mcpmcp.GetPromptResult{
			Description: fmt.Sprintf("Bug report in %s", ident),
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: mcpmcp.TextContent{Type: "text", Text: body},
			}},
		}, nil
	})

	if containerWithDatesType == nil {
		return
	}

	ctSlug := strings.ToLower(containerWithDatesType.Slug)
	createCT := fmt.Sprintf("tack_create_%s", ctSlug)

	// tack_sprint_setup
	s.AddPrompt(mcpmcp.Prompt{
		Name:        "tack_sprint_setup",
		Description: fmt.Sprintf("Sprint setup: creates a %s and populates it with %s.", ctSlug, wfPlural),
		Arguments: []mcpmcp.PromptArgument{
			{Name: "container_identifier", Description: "Container short identifier e.g. ENG", Required: true},
			{Name: "sprint_name", Description: "Name for the new sprint e.g. Sprint 3", Required: true},
			{Name: "start_date", Description: "Start date YYYY-MM-DD", Required: false},
			{Name: "end_date", Description: "End date YYYY-MM-DD", Required: false},
		},
	}, func(_ context.Context, req mcpmcp.GetPromptRequest) (*mcpmcp.GetPromptResult, error) {
		args := req.Params.Arguments
		ident := args["container_identifier"]
		sprintName := args["sprint_name"]
		startDate := args["start_date"]
		endDate := args["end_date"]
		dateClause := ""
		if startDate != "" || endDate != "" {
			dateClause = " with start_date='" + startDate + "' end_date='" + endDate + "'"
		}
		body := fmt.Sprintf("Sprint setup for %s:\n", ident) +
			fmt.Sprintf("1. Read tack://project/%s to confirm states.\n", ident) +
			fmt.Sprintf("2. Call %s with name='%s'%s.\n", createCT, sprintName, dateClause) +
			fmt.Sprintf("3. Call %s filtered to backlog/unstarted.\n", listWF) +
			fmt.Sprintf("4. Select %s for the sprint.\n", wfPlural) +
			fmt.Sprintf("5. Assign unassigned items using %s.", updateWF)
		return &mcpmcp.GetPromptResult{
			Description: fmt.Sprintf("Sprint setup: %s in %s", sprintName, ident),
			Messages: []mcpmcp.PromptMessage{{
				Role:    "user",
				Content: mcpmcp.TextContent{Type: "text", Text: body},
			}},
		}, nil
	})
}
