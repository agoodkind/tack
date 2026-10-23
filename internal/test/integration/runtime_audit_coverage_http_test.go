package integration_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// routeCase creates an HTTP request case that requires the specified status and
// audit event. Authenticated requests call tack_list_workspaces through MCP;
// other requests use the system organization.
func routeCase(method, path string, authenticated bool, status int, verb audit.Verb, outcome audit.Outcome) coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			requestID := newRequestID(t, "coverage-route")
			bearer, body, orgID := "", []byte(nil), audit.SystemOrgID()
			if authenticated {
				bearer, orgID = env.server.token, env.server.orgID
				body = rpcBody(t, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
			}
			response := env.server.send(t, method, path, bearer, requestID, body)
			if response.status != status {
				t.Fatalf("The route did not return the case's expected HTTP status; the method, path, observed status, and response body follow: %s %s: %d %s", method, path, response.status, response.body)
			}
			return coverageEvidence{orgID: orgID, requestID: requestID, actorID: uuid.Nil, outcome: "", outbox: nil}
		},
		verb: verb, outcome: outcome, check: nil,
	}
}

// addHTTPCases adds audit cases for MCP routes, health and diagnostics
// requests, and an unknown route.
func addHTTPCases(cases map[string]coverageCase) {
	cases["http route /mcp"] = routeCase(http.MethodPost, "/mcp", true, http.StatusOK, audit.VerbWorkspaceList, audit.OutcomeCompleted)
	cases["http route /mcp/"] = routeCase(http.MethodPost, "/mcp/", true, http.StatusOK, audit.VerbWorkspaceList, audit.OutcomeCompleted)
	cases["http route /healthz"] = routeCase(http.MethodGet, "/healthz", false, http.StatusOK, audit.VerbHTTPHealthRead, audit.OutcomeCompleted)
	cases["http route /debug/vars"] = routeCase(http.MethodGet, "/debug/vars", false, http.StatusOK, audit.VerbHTTPDiagnosticsRead, audit.OutcomeCompleted)
	cases["http route /"] = routeCase(http.MethodGet, "/coverage-unregistered", false, http.StatusNotFound, audit.VerbHTTPRouteUnknown, audit.OutcomeRefused)
}
