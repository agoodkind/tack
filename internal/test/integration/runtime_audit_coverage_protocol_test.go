//go:build runtimeaudit

package integration_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// rpcCall sends an authenticated MCP request and requires the specified HTTP
// status. It returns evidence for the request in the server's organization.
func (env *coverageEnv) rpcCall(
	t *testing.T,
	method string,
	params map[string]any,
	status int,
) coverageEvidence {
	t.Helper()
	requestID := newRequestID(t, "coverage-method")
	response := env.server.rpc(t, requestID, method, params)
	if response.status != status {
		t.Fatalf(
			"The MCP request did not return the case's expected HTTP status; the method, observed status, and response body follow: %s: %d %s",
			method,
			response.status,
			response.body,
		)
	}
	return coverageEvidence{
		orgID:     env.server.orgID,
		requestID: requestID,
		actorID:   uuid.Nil,
		outcome:   "",
		outbox:    nil,
		body:      response.body,
		targetID:  uuid.Nil,
	}
}

// methodCase creates an MCP method case that requires HTTP 200 and a completed
// audit event. It selects the system organization when systemOrg is true and
// the server's organization otherwise.
func methodCase(name string, systemOrg bool, verb audit.Verb, params map[string]any) coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			evidence := env.rpcCall(t, name, params, http.StatusOK)
			if systemOrg {
				evidence.orgID = audit.SystemOrgID()
			}
			return evidence
		},
		verb:    verb,
		outcome: audit.OutcomeCompleted,
		check:   nil,
	}
}

// addProtocolCases adds audit cases for recorded MCP methods and the advertised
// prompt and resource.
func addProtocolCases(cases map[string]coverageCase) {
	prompt := map[string]any{"name": "tack_getting_started"}
	cases["mcp method prompts/get"] = methodCase(
		"prompts/get",
		false,
		audit.VerbMCPPromptInvoked,
		prompt,
	)
	cases["mcp prompt tack_getting_started"] = methodCase(
		"prompts/get",
		false,
		audit.VerbMCPPromptInvoked,
		prompt,
	)
	resource := map[string]any{"uri": "tack://getting-started"}
	cases["mcp method resources/read"] = methodCase(
		"resources/read",
		false,
		audit.VerbMCPResourceRead,
		resource,
	)
	cases["mcp resource tack://getting-started"] = methodCase(
		"resources/read",
		false,
		audit.VerbMCPResourceRead,
		resource,
	)
	cases["mcp method tools/call"] = toolCase(
		"tack_list_workspaces",
		audit.VerbWorkspaceList,
		audit.OutcomeCompleted,
		func(*testing.T, *coverageEnv) map[string]any { return map[string]any{} },
	)
}
