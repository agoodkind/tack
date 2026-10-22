package integration_test

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// protocolCase is one request to a registered route and the ledger event it must produce.
type protocolCase struct {
	name, method, path, bearer string
	body                       []byte
	status                     int
	verb                       audit.Verb
	outcome                    audit.Outcome
	code                       string
	authenticated              bool
}

// TestRuntimeAuditProtocolCoverage checks that each request from protocolCases produces
// exactly one ledger event. It checks each event's verb, outcome, actor, and response body digest.
func TestRuntimeAuditProtocolCoverage(t *testing.T) {
	server := startPublicServer(t, nil)
	cases := protocolCases(t, server)
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
			orgID, actorID = server.orgID, server.userID
		}
		row := ledger.row(t, orgID, requestIDs[index])
		requireReadRow(t, row, current.verb, current.outcome, current.code, responses[index].body)
		if row.ActorID != actorID {
			t.Fatalf("%s event %s actor = %s, want %s", current.name, row.EventID, row.ActorID, actorID)
		}
	}
}

// protocolCases returns the requests that TestRuntimeAuditProtocolCoverage sends.
func protocolCases(t *testing.T, server *publicServer) []protocolCase {
	t.Helper()
	rpc := func(name, method string, params map[string]any, verb audit.Verb, outcome audit.Outcome, code string) protocolCase {
		return protocolCase{
			name: name, method: http.MethodPost, path: "/mcp", bearer: server.token, body: rpcBody(t, method, params),
			status: http.StatusOK, verb: verb, outcome: outcome, code: code, authenticated: true,
		}
	}
	route := func(name, path string, status int, verb audit.Verb, outcome audit.Outcome, code string) protocolCase {
		return protocolCase{
			name: name, method: http.MethodGet, path: path, bearer: "", body: nil,
			status: status, verb: verb, outcome: outcome, code: code, authenticated: false,
		}
	}
	initialize := map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "runtime-audit-test", "version": "1.0.0"},
	}
	malformed := rpc("malformed body", "", nil, audit.VerbMCPRequestRejected, audit.OutcomeRefused, "parse_error")
	malformed.body = []byte(`{"jsonrpc": "2.0", "id": `)
	malformed.status = http.StatusBadRequest
	return []protocolCase{
		rpc("initialize", "initialize", initialize, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("ping", "ping", map[string]any{}, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("tools/list", "tools/list", map[string]any{}, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("prompts/list", "prompts/list", map[string]any{}, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("prompts/get", "prompts/get", map[string]any{"name": "tack_getting_started"},
			audit.VerbMCPPromptInvoked, audit.OutcomeCompleted, ""),
		rpc("resources/list", "resources/list", map[string]any{}, audit.VerbMCPProtocolRead, audit.OutcomeCompleted, ""),
		rpc("resources/read", "resources/read", map[string]any{"uri": "tack://getting-started"},
			audit.VerbMCPResourceRead, audit.OutcomeCompleted, ""),
		rpc("unknown method", "tack/unknown", map[string]any{}, audit.VerbMCPRequestRejected, audit.OutcomeRefused, "method_not_found"),
		rpc("unknown tool", "tools/call", map[string]any{"name": "tack_unknown_operation", "arguments": map[string]any{}},
			audit.VerbMCPRequestRejected, audit.OutcomeRefused, "invalid_params"),
		malformed,
		route("health check", "/healthz", http.StatusOK, audit.VerbHTTPHealthRead, audit.OutcomeCompleted, ""),
		route("diagnostics", "/debug/vars", http.StatusOK, audit.VerbHTTPDiagnosticsRead, audit.OutcomeCompleted, ""),
		route("unknown route", "/unknown-route", http.StatusNotFound, audit.VerbHTTPRouteUnknown, audit.OutcomeRefused, "not_found"),
	}
}
