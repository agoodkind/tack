//go:build runtimeaudit

package integration_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// coverageNodeType pairs a node type slug with the plural form that its list
// tool uses.
type coverageNodeType struct {
	slug, plural string
}

// coverageNodeTypes lists the node types that receive generated MCP tool cases
// in fixture creation order.
var coverageNodeTypes = []coverageNodeType{
	{"project", "projects"},
	{"state", "states"},
	{"label", "labels"},
	{"cycle", "cycles"},
	{"module", "modules"},
	{"epic", "epics"},
	{"issue", "issues"},
	{"comment", "comments"},
	{"activity", "activities"},
}

// toolCall invokes an MCP tool with a unique request ID and returns audit
// evidence and the tool's text result.
func (env *coverageEnv) toolCall(t *testing.T, tool string, arguments map[string]any) (coverageEvidence, string) {
	t.Helper()
	requestID := newRequestID(t, "coverage-"+tool)
	response := env.server.rpc(t, requestID, "tools/call", map[string]any{"name": tool, "arguments": arguments})
	evidence := coverageEvidence{
		orgID: env.server.orgID, requestID: requestID, actorID: uuid.Nil, outcome: "", outbox: nil,
		body: response.body, targetID: argumentNodeID(t, arguments),
	}
	return evidence, toolText(t, response)
}

// toolCase creates a tool invocation case with an expected audit verb and
// outcome. It derives arguments from the test environment.
func toolCase(tool string, verb audit.Verb, outcome audit.Outcome, arguments func(t *testing.T, env *coverageEnv) map[string]any) coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			evidence, _ := env.toolCall(t, tool, arguments(t, env))
			return evidence
		},
		verb: verb, outcome: outcome, check: nil,
	}
}

// addNodeToolCases adds create, get, list, update, and delete audit cases for
// each entry in coverageNodeTypes.
func addNodeToolCases(cases map[string]coverageCase) {
	for _, nodeType := range coverageNodeTypes {
		cases["mcp tool tack_create_"+nodeType.slug] = toolCase("tack_create_"+nodeType.slug, audit.VerbNodeCreate, audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) map[string]any {
				return env.fixture(t).createArguments(nodeType.slug)
			})
		cases["mcp tool tack_get_"+nodeType.slug] = toolCase("tack_get_"+nodeType.slug, audit.VerbNodeRead, audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) map[string]any {
				return env.fixture(t).nodeArguments(nodeType.slug)
			})
		cases["mcp tool tack_list_"+nodeType.plural] = toolCase("tack_list_"+nodeType.plural, audit.VerbNodeList, audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) map[string]any {
				return env.fixture(t).scopeArguments(nodeType.slug)
			})
		cases["mcp tool tack_update_"+nodeType.slug] = toolCase("tack_update_"+nodeType.slug, audit.VerbNodeUpdate, audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) map[string]any {
				arguments := env.fixture(t).nodeArguments(nodeType.slug)
				arguments["name"] = "Coverage update " + uuid.NewString()[:8]
				return arguments
			})
		cases["mcp tool tack_delete_"+nodeType.slug] = toolCase("tack_delete_"+nodeType.slug, audit.VerbNodeDelete, audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) map[string]any {
				fixture := env.fixture(t)
				return map[string]any{"workspace_reference": env.server.workspace, "node_id": fixture.create(t, nodeType.slug).String()}
			})
	}
}

// addMCPCases adds audit cases for MCP tools and protocol operations.
func addMCPCases(cases map[string]coverageCase) {
	addNodeToolCases(cases)
	workspace := func(t *testing.T, env *coverageEnv) map[string]any {
		return map[string]any{"workspace_reference": env.server.workspace}
	}
	cases["mcp tool tack_list_workspaces"] = toolCase("tack_list_workspaces", audit.VerbWorkspaceList, audit.OutcomeCompleted,
		func(*testing.T, *coverageEnv) map[string]any { return map[string]any{} })
	cases["mcp tool tack_describe_workspace"] = toolCase("tack_describe_workspace", audit.VerbWorkspaceDescribe, audit.OutcomeCompleted, workspace)
	cases["mcp tool tack_list_members"] = toolCase("tack_list_members", audit.VerbMembersList, audit.OutcomeCompleted, workspace)
	cases["mcp tool tack_list_property_defs"] = toolCase("tack_list_property_defs", audit.VerbPropertyDefsList, audit.OutcomeCompleted, workspace)
	issueOnly := func(t *testing.T, env *coverageEnv) map[string]any {
		return map[string]any{"node_id": env.fixture(t).nodes["issue"].String()}
	}
	cases["mcp tool tack_get_properties"] = toolCase("tack_get_properties", audit.VerbPropsRead, audit.OutcomeCompleted, issueOnly)
	cases["mcp tool tack_list_relationships"] = toolCase("tack_list_relationships", audit.VerbRelationshipList, audit.OutcomeCompleted, issueOnly)
	cases["mcp tool tack_add_relationship"] = toolCase("tack_add_relationship", audit.VerbRelationshipAdd, audit.OutcomeCompleted,
		func(t *testing.T, env *coverageEnv) map[string]any { return env.fixture(t).labelEdge(t) })
	cases["mcp tool tack_remove_relationship"] = coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			arguments := env.fixture(t).labelEdge(t)
			env.toolCall(t, "tack_add_relationship", arguments)
			evidence, _ := env.toolCall(t, "tack_remove_relationship", arguments)
			return evidence
		},
		verb: audit.VerbRelationshipRemove, outcome: audit.OutcomeCompleted, check: nil,
	}
	for _, setter := range []string{"issue", "epic"} {
		cases["mcp tool tack_set_"+setter+"_state"] = toolCase("tack_set_"+setter+"_state", audit.VerbNodeUpdate, audit.OutcomeCompleted,
			func(t *testing.T, env *coverageEnv) map[string]any { return env.fixture(t).stateArguments(setter) })
	}
	cases["mcp tool tack_search"] = coverageCase{run: runSearchCase, verb: audit.VerbNodeSearch, outcome: audit.OutcomeCompleted, check: nil}
	addProtocolCases(cases)
}

// runSearchCase invokes tack_search and validates its tool result. It expects
// success with OpenSearch and an error result with a failed audit outcome
// without OpenSearch.
func runSearchCase(t *testing.T, env *coverageEnv) coverageEvidence {
	t.Helper()
	requestID := newRequestID(t, "coverage-search")
	response := env.server.rpc(t, requestID, "tools/call", map[string]any{
		"name": "tack_search", "arguments": map[string]any{"workspace_reference": env.server.workspace, "query": "coverage"},
	})
	evidence := coverageEvidence{
		orgID: env.server.orgID, requestID: requestID, actorID: uuid.Nil, outcome: "", outbox: nil,
		body: response.body, targetID: uuid.Nil,
	}
	if env.searchEnabled {
		toolText(t, response)
		return evidence
	}
	if response.status != http.StatusOK || !strings.Contains(string(response.body), `"isError":true`) {
		t.Fatalf("tack_search did not return the expected HTTP 200 response with isError=true while OpenSearch was disabled; the observed status and body follow: %d %s", response.status, response.body)
	}
	evidence.outcome = audit.OutcomeFailed
	return evidence
}
