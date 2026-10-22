package integration_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
)

// TestRuntimeAuditPublicRead creates and reads a project through the MCP route.
// It checks the read event against the verified user, the project identity,
// and the response body digest. It also checks that the create request's ledger
// events share a non-nil ParentOperationID that differs from each event's
// OperationID.
func TestRuntimeAuditPublicRead(t *testing.T) {
	server := startPublicServer(t, nil)
	identifier := "audit-read-" + uuid.NewString()[:8]
	createID := newRequestID(t, "create")
	created := server.rpc(t, createID, "tools/call", map[string]any{
		"name": "tack_create_project",
		"arguments": map[string]any{
			"workspace_reference": server.workspace, "name": identifier,
			"properties": map[string]any{"identifier": identifier},
		},
	})
	projectID := uuid.MustParse(rawID(t, toolText(t, created)))
	requestID := newRequestID(t, "get")
	read := server.rpc(t, requestID, "tools/call", map[string]any{
		"name":      "tack_get_project",
		"arguments": map[string]any{"workspace_reference": server.workspace, "node_id": projectID.String()},
	})
	if !strings.Contains(toolText(t, read), identifier) {
		t.Fatalf("tack_get_project response does not contain project %s:\n%s", identifier, read.body)
	}
	ledger := startPublicLedger(t, server)
	row := ledger.row(t, server.orgID, requestID)
	requireReadRow(t, row, audit.VerbNodeRead, audit.OutcomeCompleted, "", read.body)
	if row.ActorID != server.userID || row.Context.Tool != "tack_get_project" || row.Context.RPC != "tools/call" {
		t.Fatalf("event %s actor = %s, tool = %q, method = %q, want %s tack_get_project tools/call",
			row.EventID, row.ActorID, row.Context.Tool, row.Context.RPC, server.userID)
	}
	if row.Context.Read.PayloadFormat != read.header.Get("Content-Type") || row.Context.Read.Count != 1 {
		t.Fatalf("event %s read evidence = %+v, want format %q and one identity",
			row.EventID, row.Context.Read, read.header.Get("Content-Type"))
	}
	targets := ledger.manifestTargets(t, row)
	if len(targets) != 1 || targets[0].Kind != audit.TargetKindNode || targets[0].ID != projectID {
		t.Fatalf("event %s manifest = %+v, want project %s", row.EventID, targets, projectID)
	}
	parentID := uuid.Nil
	for _, change := range ledger.changeRows(t, server.orgID, createID) {
		eventContext := change.Context
		if eventContext.ParentOperationID == uuid.Nil || eventContext.ParentOperationID == eventContext.OperationID {
			t.Fatalf("change event %s ParentOperationID = %s, want a non-nil ID different from OperationID %s",
				change.EventID, eventContext.ParentOperationID, eventContext.OperationID)
		}
		if parentID != uuid.Nil && eventContext.ParentOperationID != parentID {
			t.Fatalf("change event %s ParentOperationID = %s, want the shared parent ID %s", change.EventID, eventContext.ParentOperationID, parentID)
		}
		parentID = eventContext.ParentOperationID
	}
}

// TestRuntimeAuditEarlyRefusal checks ledger events for requests that the server rejects before
// authentication. It checks the unauthenticated actor, system organization, rejection code,
// and response body digest.
func TestRuntimeAuditEarlyRefusal(t *testing.T) {
	server := startPublicServer(t, nil)
	listWorkspaces := rpcBody(t, "tools/call", map[string]any{"name": "tack_list_workspaces", "arguments": map[string]any{}})
	invalidBearer := "invalid-bearer-" + uuid.NewString()
	invalidReason := auth.RejectionBearerRefused
	if server.config.Env == "development" {
		invalidReason = auth.RejectionDevIdentityInvalid
	}
	cases := []struct {
		name, method, bearer, code string
		verb                       audit.Verb
		status                     int
	}{
		{"missing bearer", http.MethodPost, "", auth.RejectionMissingAuthorization, audit.VerbAuthLoginFailed, http.StatusUnauthorized},
		{"invalid bearer", http.MethodPost, invalidBearer, invalidReason, audit.VerbAuthTokenRejected, http.StatusUnauthorized},
		{"GET method", http.MethodGet, "", "method_not_allowed", audit.VerbMCPRequestRejected, http.StatusMethodNotAllowed},
	}
	requestIDs := make([]string, len(cases))
	responses := make([]publicResponse, len(cases))
	for index, current := range cases {
		requestIDs[index] = newRequestID(t, "refusal")
		responses[index] = server.send(t, current.method, "/mcp", current.bearer, requestIDs[index], listWorkspaces)
		if responses[index].status != current.status {
			t.Fatalf("%s status = %d, want %d: %s", current.name, responses[index].status, current.status, responses[index].body)
		}
	}
	ledger := startPublicLedger(t, server)
	for index, current := range cases {
		row := ledger.row(t, audit.SystemOrgID(), requestIDs[index])
		requireReadRow(t, row, current.verb, audit.OutcomeRefused, current.code, responses[index].body)
		if row.ActorID != uuid.Nil || row.Context.Target == nil || row.Context.Target.URI != "/mcp" {
			t.Fatalf("%s event %s actor = %s, target = %+v, want the unauthenticated actor and /mcp",
				current.name, row.EventID, row.ActorID, row.Context.Target)
		}
		if current.method == http.MethodPost && row.Context.Tool != "tack_list_workspaces" {
			t.Fatalf("%s event %s tool = %q, want tack_list_workspaces", current.name, row.EventID, row.Context.Tool)
		}
		stored, err := json.Marshal(row)
		if err != nil {
			t.Fatalf("json.Marshal failed for event %s: %v", row.EventID, err)
		}
		if strings.Contains(string(stored), invalidBearer) {
			t.Fatalf("%s event %s stores the bearer value", current.name, row.EventID)
		}
	}
}

// toolText returns the text content of a successful tools/call response.
func toolText(t *testing.T, response publicResponse) string {
	t.Helper()
	if response.status != http.StatusOK {
		t.Fatalf("tools/call status = %d: %s", response.status, response.body)
	}
	var decoded struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(response.body, &decoded); err != nil {
		t.Fatalf("json.Unmarshal failed for the tools/call response: %v\n%s", err, response.body)
	}
	if decoded.Result.IsError || len(decoded.Result.Content) == 0 {
		t.Fatalf("tools/call returned an error or did not return any content: %s", response.body)
	}
	return decoded.Result.Content[0].Text
}

// rawID returns the node ID after the last "Raw id" field in tool output.
func rawID(t *testing.T, text string) string {
	t.Helper()
	const marker = "- Raw id: `"
	start := strings.LastIndex(text, marker)
	if start == -1 {
		t.Fatalf("tool output does not contain a raw node ID:\n%s", text)
	}
	remainder := text[start+len(marker):]
	end := strings.IndexByte(remainder, '`')
	if end == -1 {
		t.Fatalf("tool output contains an unterminated raw node ID:\n%s", text)
	}
	return remainder[:end]
}
