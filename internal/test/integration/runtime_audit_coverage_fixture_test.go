package integration_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// coverageFixture stores test node IDs by node type and the environment that
// creates them.
type coverageFixture struct {
	env   *coverageEnv
	nodes map[string]uuid.UUID
}

// newCoverageFixture creates one node for each entry in coverageNodeTypes
// through MCP and stores its ID by node type.
func newCoverageFixture(t *testing.T, env *coverageEnv) *coverageFixture {
	t.Helper()
	fixture := &coverageFixture{env: env, nodes: map[string]uuid.UUID{}}
	for _, nodeType := range coverageNodeTypes {
		fixture.nodes[nodeType.slug] = fixture.create(t, nodeType.slug)
	}
	return fixture
}

// fixture returns the environment's test nodes and fails if initialization has
// not created them.
func (env *coverageEnv) fixture(t *testing.T) *coverageFixture {
	t.Helper()
	if env.nodes == nil {
		t.Fatal("The coverage environment does not contain the required node fixture; initialize it before running a case.")
	}
	return env.nodes
}

// create creates a node through its MCP tool and returns the UUID from the tool
// result.
func (fixture *coverageFixture) create(t *testing.T, slug string) uuid.UUID {
	t.Helper()
	_, text := fixture.env.toolCall(t, "tack_create_"+slug, fixture.createArguments(slug))
	return uuid.MustParse(rawID(t, text))
}

// createArguments builds scoped node creation arguments with a unique name. It
// also supplies an identifier property for projects.
func (fixture *coverageFixture) createArguments(slug string) map[string]any {
	suffix := strings.ToUpper(strings.ReplaceAll(uuid.NewString()[:8], "-", ""))
	arguments := fixture.scopeArguments(slug)
	arguments["name"] = "Coverage " + slug + " " + suffix
	if slug == "project" {
		arguments["properties"] = map[string]any{"identifier": "C" + suffix}
	}
	return arguments
}

// scopeArguments supplies the workspace reference and any parent references the
// node type requires. Projects and labels use the workspace; comments and
// activities also use the project and issue; other types also use the project.
func (fixture *coverageFixture) scopeArguments(slug string) map[string]any {
	arguments := map[string]any{"workspace_reference": fixture.env.server.workspace}
	switch slug {
	case "project", "label":
	case "comment", "activity":
		arguments["project_reference"] = fixture.nodes["project"].String()
		arguments["issue_reference"] = fixture.nodes["issue"].String()
	default:
		arguments["project_reference"] = fixture.nodes["project"].String()
	}
	return arguments
}

// nodeArguments returns the workspace reference and stored node ID for the
// given node type.
func (fixture *coverageFixture) nodeArguments(slug string) map[string]any {
	return map[string]any{"workspace_reference": fixture.env.server.workspace, "node_id": fixture.nodes[slug].String()}
}

// stateArguments builds arguments that set the stored node's state to Done
// within its workspace and project.
func (fixture *coverageFixture) stateArguments(slug string) map[string]any {
	return map[string]any{
		"workspace_reference": fixture.env.server.workspace, "project_reference": fixture.nodes["project"].String(),
		slug + "_reference": fixture.nodes[slug].String(), "state": "Done",
	}
}

// labelEdge creates a label and returns arguments for a labeled_with
// relationship from the stored issue to that label.
func (fixture *coverageFixture) labelEdge(t *testing.T) map[string]any {
	t.Helper()
	return map[string]any{
		"source_id": fixture.nodes["issue"].String(), "relation_type": "labeled_with",
		"target_id": fixture.create(t, "label").String(),
	}
}
