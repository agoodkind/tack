package integration_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// protocolCase defines a request to a registered route and its expected ledger event.
// When authenticated is true, the test expects the event to identify the caller.
// When tenant is true, the test expects the event in the caller's organization.
type protocolCase struct {
	name, method, path, bearer string
	body                       []byte
	status                     int
	verb                       audit.Verb
	outcome                    audit.Outcome
	code                       string
	authenticated              bool
	tenant                     bool
}

func TestRuntimeAuditProtocolCoverage(t *testing.T) {
	server := startPublicServer(t, nil)
	cases := append(protocolCases(t, server), refusalCases(t, server, server.foreignProject(t))...)
	requestIDs := make([]string, len(cases))
	responses := make([]publicResponse, len(cases))
	for index, current := range cases {
		requestIDs[index] = newRequestID(t, "protocol")
		responses[index] = server.send(t, current.method, current.path, current.bearer, requestIDs[index], current.body)
		if responses[index].status != current.status {
			t.Fatalf("%s status = %d, want %d: %s", current.name, responses[index].status, current.status, responses[index].body)
		}
	}
	ledger := startPublicLedger(t, server)
	for index, current := range cases {
		orgID, actorID := audit.SystemOrgID(), uuid.Nil
		if current.authenticated {
			actorID = server.userID
		}
		if current.tenant {
			orgID = server.orgID
		}
		row := ledger.row(t, orgID, requestIDs[index])
		requireReadRow(t, row, current.verb, current.outcome, current.code, responses[index].body)
		if row.ActorID != actorID {
			t.Fatalf("%s event %s actor = %s, want %s", current.name, row.EventID, row.ActorID, actorID)
		}
	}
}

// refusalCases returns authenticated tool requests with unsuccessful
// outcomes. A read of foreignProjectID, a project in an organization that
// the caller does not belong to, is denied access. A read of a new UUID is an
// absent target. A search fails because the test server does not run a
// search backend.
func refusalCases(t *testing.T, server *publicServer, foreignProjectID string) []protocolCase {
	t.Helper()
	call := func(name, tool string, arguments map[string]any, verb audit.Verb, outcome audit.Outcome, code string) protocolCase {
		arguments["workspace_reference"] = server.workspace
		return protocolCase{
			name: name, method: http.MethodPost, path: "/mcp", bearer: server.token,
			body:   rpcBody(t, "tools/call", map[string]any{"name": tool, "arguments": arguments}),
			status: http.StatusOK, verb: verb, outcome: outcome, code: code, authenticated: true, tenant: true,
		}
	}
	return []protocolCase{
		call("denied access", "tack_get_project", map[string]any{"node_id": foreignProjectID},
			audit.VerbNodeRead, audit.OutcomeRefused, "access_denied"),
		call("absent target", "tack_get_project", map[string]any{"node_id": uuid.NewString()},
			audit.VerbNodeRead, audit.OutcomeRefused, "not_found"),
		call("backend error", "tack_search", map[string]any{"query": "anything"},
			audit.VerbNodeSearch, audit.OutcomeFailed, "search_unavailable"),
	}
}

func protocolCases(t *testing.T, server *publicServer) []protocolCase {
	t.Helper()
	rpc := func(name, method string, params map[string]any, tenant bool, verb audit.Verb, outcome audit.Outcome, code string) protocolCase {
		return protocolCase{
			name: name, method: http.MethodPost, path: "/mcp", bearer: server.token, body: rpcBody(t, method, params),
			status: http.StatusOK, verb: verb, outcome: outcome, code: code, authenticated: true, tenant: tenant,
		}
	}
	route := func(name, method, path string, status int, verb audit.Verb, outcome audit.Outcome, code string) protocolCase {
		return protocolCase{
			name: name, method: method, path: path, bearer: "", body: nil,
			status: status, verb: verb, outcome: outcome, code: code, authenticated: false, tenant: false,
		}
	}
	initialize := map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "runtime-audit-test", "version": "1.0.0"},
	}
	malformed := rpc("malformed body", "", nil, false, audit.VerbMCPRequestRejected, audit.OutcomeRefused, "parse_error")
	malformed.body = []byte(`{"jsonrpc": "2.0", "id": `)
	malformed.status = http.StatusBadRequest
	return []protocolCase{
		rpc("initialize", "initialize", initialize, false, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("ping", "ping", map[string]any{}, false, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("tools/list", "tools/list", map[string]any{}, true, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("prompts/list", "prompts/list", map[string]any{}, false, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("prompts/get", "prompts/get", map[string]any{"name": "tack_getting_started"}, true,
			audit.VerbMCPPromptInvoked, audit.OutcomeCompleted, ""),
		rpc("resources/list", "resources/list", map[string]any{}, false, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("resources/read", "resources/read", map[string]any{"uri": "tack://getting-started"}, true,
			audit.VerbMCPResourceRead, audit.OutcomeCompleted, ""),
		rpc("unknown method", "tack/unknown", map[string]any{}, false,
			audit.VerbMCPRequestRejected, audit.OutcomeRefused, "method_not_found"),
		rpc("unknown tool", "tools/call", map[string]any{"name": "tack_unknown_operation", "arguments": map[string]any{}}, false,
			audit.VerbMCPRequestRejected, audit.OutcomeRefused, "invalid_params"),
		malformed,
		route("health check", http.MethodGet, "/healthz", http.StatusOK, audit.VerbHTTPHealthRead, audit.OutcomeCompleted, ""),
		route("health check method", http.MethodPost, "/healthz", http.StatusMethodNotAllowed,
			audit.VerbHTTPHealthRead, audit.OutcomeRefused, "method_not_allowed"),
		route("diagnostics", http.MethodGet, "/debug/vars", http.StatusOK, audit.VerbHTTPDiagnosticsRead, audit.OutcomeCompleted, ""),
		route("unknown route", http.MethodGet, "/unknown-route", http.StatusNotFound,
			audit.VerbHTTPRouteUnknown, audit.OutcomeRefused, "not_found"),
	}
}
