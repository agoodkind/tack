package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestRuntimeAuditTokenUseDurableBeforeSuccess authenticates one MCP request
// with a real API token through auth.Bearer, with development authentication
// off. The buffered queue does not flush during the test. The durable path
// must store the auth.token_used event. The test does not stop a process.
//
// Before the relay starts, the FoundationDB outbox must contain the event for
// the request with the token owner as actor and the token ID. After the relay
// and consumer run, the ledger row must contain the owner as actor, the
// tack:api_token/ target of the token, the request ID, the completed outcome,
// and the operation ID shared with the read row for the request.
//
// api_tokens.last_used for the token must equal the event time. The consumer
// stores the newest event time, and this request is the only token use.
func TestRuntimeAuditTokenUseDurableBeforeSuccess(t *testing.T) {
	stack := startRuntimeAuditStack(t)

	t.Setenv("ENV", "production")

	cfg := publicConfig(t)
	cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = testenv.Kafka(t), stack.topic
	cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", false
	cfg.AuditReadFlushInterval = tokenUseNeverFlushInterval
	server := buildPublicServer(t, cfg, nil)

	requestID := newRequestID(t, "token-use")

	response := server.rpc(t, requestID, "tools/list", map[string]any{})
	if response.status != 200 {
		t.Fatalf("tools/list status = %d, want 200: %s", response.status, response.body)
	}

	tokenID := fixtureTokenID(t, server)

	stored := outboxTokenUsed(t, server, requestID)
	if stored.Actor.ID != server.userID || stored.Context.APITokenID != tokenID {
		t.Fatalf(
			"outbox auth.token_used actor = %s, token = %s, want %s and %s",
			stored.Actor.ID,
			stored.Context.APITokenID,
			server.userID,
			tokenID,
		)
	}

	ledger := startPublicLedgerOn(t, server, stack)
	rows := ledger.requestRows(t, server.orgID, requestID, hasAuthAndOther)
	row := requireTokenUsedRow(t, rows, server.userID)

	requireTokenUsedRowFields(t, row, server.userID, tokenID, requestID)

	for _, other := range rows {
		if other.Action != string(audit.VerbAuthTokenUsed) &&
			other.Context.OperationID != row.Context.OperationID {
			t.Fatalf(
				"%s row operation ID = %s, want the auth.token_used operation ID %s",
				other.Action,
				other.Context.OperationID,
				row.Context.OperationID,
			)
		}
	}

	requireLastUsed(t, tokenID, row.EventTime)
}
