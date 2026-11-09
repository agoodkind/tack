package integration_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/testenv"
)

const (
	// tokenUseUnreachableBroker is an address without a Kafka broker.
	tokenUseUnreachableBroker = "localhost:1"
	// tokenUseOutageWait is how long the relay runs against the unreachable broker.
	tokenUseOutageWait = 10 * time.Second
	// tokenUseRelayPoll is the outbox poll interval of the test relay.
	tokenUseRelayPoll = 100 * time.Millisecond
	// tokenUseRelayBatch is the outbox batch size of the test relay.
	tokenUseRelayBatch = 64
)

// TestRuntimeAuditTokenUseSurvivesBrokerOutage authenticates one request with a
// real API token while the only relay in the test points at a Kafka address
// without a broker. The request must succeed. Throughout the outage, the
// outbox must contain the auth.token_used event and the ledger must not
// contain it. After a relay with a working broker starts, the ledger row must
// contain the actor, token target, request ID, operation ID, and outcome, and
// api_tokens.last_used must equal the event time.
func TestRuntimeAuditTokenUseSurvivesBrokerOutage(t *testing.T) {
	stack := startRuntimeAuditStack(t)

	t.Setenv("ENV", "production")

	cfg := publicConfig(t)
	cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = testenv.Kafka(t), stack.topic
	cfg.AuditWriterDSN, cfg.AuditAllowUnrecorded = "", false
	cfg.AuditReadFlushInterval = tokenUseNeverFlushInterval
	server := buildPublicServer(t, cfg, nil)

	requestID := newRequestID(t, "token-use-outage")

	response := server.rpc(t, requestID, "tools/list", map[string]any{})
	if response.status != 200 {
		t.Fatalf("tools/list status = %d, want 200: %s", response.status, response.body)
	}

	outage, err := auditrelay.Open(
		t.Context(),
		auditrelay.Config{
			Brokers: []string{
				tokenUseUnreachableBroker,
			},
			Topic:                 stack.topic,
			YugabyteDSN:           testenv.Ledger(t),
			FDBClusterFile:        server.clusterFile,
			FDBTransactionTimeout: tokenUseOutboxTimeout,
			PollInterval:          tokenUseRelayPoll,
			BatchSize:             tokenUseRelayBatch,
			LeaseDuration:         0,
			AfterProduce:          nil,
			AfterLeaseAcquired:    nil,
		},
	)
	if err != nil {
		t.Fatalf("auditrelay.Open failed for the unreachable broker: %v", err)
	}

	outage.Relay.Start(t.Context())
	time.Sleep(tokenUseOutageWait)
	outboxTokenUsed(t, server, requestID)
	requireNoLedgerTokenUsed(t, stack.reader, server.orgID, requestID)

	err = outage.Relay.Close()
	if err != nil {
		t.Fatalf("closing the outage relay failed: %v", err)
	}

	outage.Pool.Close()

	ledger := startPublicLedgerOn(t, server, stack)
	rows := ledger.requestRows(t, server.orgID, requestID, hasAuthAndOther)
	row := requireTokenUsedRow(t, rows, server.userID)
	tokenID := fixtureTokenID(t, server)

	requireTokenUsedRowFields(t, row, server.userID, tokenID, requestID)
	requireLastUsed(t, tokenID, row.EventTime)
}

// requireNoLedgerTokenUsed checks that the ledger does not contain an
// auth.token_used row for requestID.
func requireNoLedgerTokenUsed(
	t *testing.T,
	reader *audit.Reader,
	orgID uuid.UUID,
	requestID string,
) {
	t.Helper()

	rows := tokenUsedRows(t, reader, orgID, requestID)
	if len(rows) != 0 {
		t.Fatalf("ledger has %d auth.token_used rows for request %s, want 0", len(rows), requestID)
	}
}
