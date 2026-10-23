//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// rpcCall sends an authenticated MCP request and requires the specified HTTP
// status. It returns evidence for the request in the server's organization.
func (env *coverageEnv) rpcCall(t *testing.T, method string, params map[string]any, status int) coverageEvidence {
	t.Helper()
	requestID := newRequestID(t, "coverage-method")
	response := env.server.rpc(t, requestID, method, params)
	if response.status != status {
		t.Fatalf("The MCP request did not return the case's expected HTTP status; the method, observed status, and response body follow: %s: %d %s", method, response.status, response.body)
	}
	return coverageEvidence{orgID: env.server.orgID, requestID: requestID, actorID: uuid.Nil, outcome: "", outbox: nil}
}

// notify sends an authenticated MCP notification without a JSON-RPC id and
// requires HTTP 202. It returns evidence for the request in the system
// organization.
func (env *coverageEnv) notify(t *testing.T, method string, params map[string]any) coverageEvidence {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		t.Fatalf("The test could not encode the expected JSON-RPC notification for %s: %v", method, err)
	}
	requestID := newRequestID(t, "coverage-notification")
	response := env.server.send(t, http.MethodPost, "/mcp", env.server.token, requestID, body)
	if response.status != http.StatusAccepted {
		t.Fatalf("The MCP notification did not return the expected HTTP 202 status; the method, observed status, and response body follow: %s: %d %s", method, response.status, response.body)
	}
	return coverageEvidence{orgID: audit.SystemOrgID(), requestID: requestID, actorID: uuid.Nil, outcome: "", outbox: nil}
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
		verb: verb, outcome: audit.OutcomeCompleted, check: nil,
	}
}

// notificationCase creates an MCP notification case that requires HTTP 202 and
// a completed protocol audit event in the system organization.
func notificationCase(name string, params map[string]any) coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			return env.notify(t, name, params)
		},
		verb: audit.VerbMCPProtocolRead, outcome: audit.OutcomeCompleted, check: nil,
	}
}

// addProtocolCases adds audit cases for recorded MCP methods and the advertised
// prompt and resource.
func addProtocolCases(cases map[string]coverageCase) {
	empty := map[string]any{}
	cases["mcp method initialize"] = methodCase("initialize", true, audit.VerbMCPProtocolRead, map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "coverage", "version": "1"},
	})
	cases["mcp method ping"] = methodCase("ping", true, audit.VerbMCPProtocolRead, empty)
	cases["mcp method tools/list"] = methodCase("tools/list", false, audit.VerbMCPProtocolRead, empty)
	cases["mcp method prompts/list"] = methodCase("prompts/list", true, audit.VerbMCPProtocolRead, empty)
	cases["mcp method resources/list"] = methodCase("resources/list", true, audit.VerbMCPProtocolRead, empty)
	cases["mcp method resources/templates/list"] = methodCase("resources/templates/list", true, audit.VerbMCPProtocolRead, empty)
	cases["mcp method notifications/initialized"] = notificationCase("notifications/initialized", empty)
	cases["mcp method notifications/cancelled"] = notificationCase("notifications/cancelled", map[string]any{"requestId": "coverage", "reason": "coverage"})
	prompt := map[string]any{"name": "tack_getting_started"}
	cases["mcp method prompts/get"] = methodCase("prompts/get", false, audit.VerbMCPPromptInvoked, prompt)
	cases["mcp prompt tack_getting_started"] = methodCase("prompts/get", false, audit.VerbMCPPromptInvoked, prompt)
	resource := map[string]any{"uri": "tack://getting-started"}
	cases["mcp method resources/read"] = methodCase("resources/read", false, audit.VerbMCPResourceRead, resource)
	cases["mcp resource tack://getting-started"] = methodCase("resources/read", false, audit.VerbMCPResourceRead, resource)
	cases["mcp method tools/call"] = toolCase("tack_list_workspaces", audit.VerbWorkspaceList, audit.OutcomeCompleted,
		func(*testing.T, *coverageEnv) map[string]any { return map[string]any{} })
}
