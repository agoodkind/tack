package integration_test

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegritySchemaSearchOmitsReferenceIDs seeds an organization and
// creates an issue with a description. It reads the stored property
// definitions and the issue node type, then calls node.RenderSearchText.
// The text must contain the description and must not contain the UUID values
// of parent_id, scope_id, or state_id.
func TestNodeIntegritySchemaSearchOmitsReferenceIDs(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	const description = "Searchable schema description"
	issue := createSchemaNode(t, env, projectID, projectID, "issue", schemaProps("description", quoted(description)))
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list property definitions: %v", err)
	}
	issueType, err := env.Stores.NodeTypes.TypeByKey(env.Ctx, env.OrgID, "issue")
	if err != nil || issueType == nil {
		t.Fatalf("read node type issue: %+v, %v", issueType, err)
	}
	render, err := node.RenderSearchText(env.Ctx, node.SearchSource{
		OrgID: env.OrgID, NodeID: issue.ID, NodeType: issueType, Name: issue.Name, Properties: issue.Props, Definitions: definitions,
	})
	if err != nil {
		t.Fatalf("render search text for issue %s: %v", issue.ID, err)
	}
	text := render.Text
	if !strings.Contains(text, description) {
		t.Fatalf("search text %q does not contain the description", text)
	}
	for _, name := range []string{"parent_id", "scope_id", "state_id"} {
		value := strings.Trim(string(issue.Props[name]), `"`)
		if value == "" {
			t.Fatalf("issue %s has no %s", issue.ID, name)
		}
		if strings.Contains(text, value) {
			t.Fatalf("search text %q contains the %s value %s", text, name, value)
		}
	}
}
