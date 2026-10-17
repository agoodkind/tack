package integration_test

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegritySchemaSearchOmitsReferenceIDs seeds an organization and
// creates an issue with a description. It reads the stored property
// definitions and calls node.EmitSearchText. The text must contain the
// description and must not contain the UUID values of parent_id, scope_id,
// or state_id.
func TestNodeIntegritySchemaSearchOmitsReferenceIDs(t *testing.T) {
	env := integration.SetupTestEnv(t)
	projectID := createSchemaProject(t, env)
	const description = "Searchable schema description"
	issue := createSchemaNode(t, env, projectID, projectID, "issue", schemaProps("description", quoted(description)))
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil {
		t.Fatalf("list property definitions: %v", err)
	}
	text, err := node.EmitSearchText(env.Ctx, issue.Name, issue.Props, definitions)
	if err != nil {
		t.Fatalf("emit search text for issue %s: %v", issue.ID, err)
	}
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
