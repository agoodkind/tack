package tools

import (
	"strings"
	"testing"

	mcpmcp "github.com/mark3labs/mcp-go/v2/mcp"
	"goodkind.io/tack/internal/domain/node"
)

func TestListToolDescribesProjectScopedQueue(t *testing.T) {
	projectType := &node.NodeType{
		TypeKey: "project",
		Slug:    "project",
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceDirectProperty,
			Property: "identifier",
		},
	}
	issueType := &node.NodeType{
		TypeKey: "issue",
		Slug:    "issue",
		Reference: node.ReferenceConfig{
			Strategy: node.ReferenceScopedSequence,
			Property: "sequence",
		},
	}
	resolver := &Resolver{
		entryPointSlug: "workspace",
		typeIndex:      map[string]*node.NodeType{"project": projectType, "issue": issueType},
	}
	tool := listTool(
		issueType,
		"issues",
		scopeRoute{
			Chain: []ScopeLevel{
				{TypeKey: "project", Slug: "project", ParamName: "project_reference"},
			},
		},
		"workspace_reference",
		resolver,
	)

	for _, want := range []string{
		"Lists issues in a project",
		"pass workspace_reference and project_reference",
		"for example TACK",
		"Optional filters apply exact property matches",
		"Use this before tack_get_issue",
	} {
		if !strings.Contains(tool.Description, want) {
			t.Fatalf("description should contain %q:\n%s", want, tool.Description)
		}
	}

	workspaceDescription := schemaDescription(t, tool, "workspace_reference")
	if !strings.Contains(workspaceDescription, "workspace reference") {
		t.Fatalf(
			"workspace_reference description = %q, want workspace reference guidance",
			workspaceDescription,
		)
	}

	projectDescription := schemaDescription(t, tool, "project_reference")
	if !strings.Contains(projectDescription, "project reference") ||
		!strings.Contains(projectDescription, "TACK") {
		t.Fatalf(
			"project_reference description = %q, want project reference guidance",
			projectDescription,
		)
	}

	filterDescription := schemaDescription(t, tool, "filters")
	if !strings.Contains(filterDescription, "Optional exact property filters") {
		t.Fatalf("filters description = %q, want exact property filter guidance", filterDescription)
	}

	assertSchemaOmits(t, tool, "workspace_slug")
	assertSchemaOmits(t, tool, "project_identifier")
}

func assertSchemaOmits(t *testing.T, tool mcpmcp.Tool, name string) {
	t.Helper()
	if _, ok := tool.InputSchema.Properties[name]; ok {
		t.Fatalf("schema should not include %s", name)
	}
}

func schemaDescription(t *testing.T, tool mcpmcp.Tool, name string) string {
	t.Helper()
	rawProperty, ok := tool.InputSchema.Properties[name]
	if !ok {
		t.Fatalf("schema should include %s", name)
	}

	property, ok := rawProperty.(map[string]any)
	if !ok {
		t.Fatalf("schema property %s has type %T, want map[string]any", name, rawProperty)
	}

	description, ok := property["description"].(string)
	if !ok {
		t.Fatalf("schema property %s should have a description", name)
	}

	return description
}
