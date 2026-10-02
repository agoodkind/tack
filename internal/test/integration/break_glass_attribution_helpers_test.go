package integration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/config"
)

const (
	// attributionDeadline bounds the wait for the relay and the consumer to
	// project the recorded events into audit.events.
	attributionDeadline = 90 * time.Second
	// attributionPollInterval is how often the relay and the consumer poll.
	attributionPollInterval = 100 * time.Millisecond
)

// breakGlassMailTests lists the tests that deliver to the Mailpit server.
var breakGlassMailTests = []string{
	"TestOpsDBSQLRecordsAgentAttribution",
	"TestOpsDBSQLRefusesSessionWithoutService",
}

// attributionExtra is the part of an operator event's extra payload these
// tests read: the choke-point op id or the break-glass attempt id, the agent
// session, and the accountable operator.
type attributionExtra struct {
	OpID       uuid.UUID            `json:"op_id"`
	AttemptID  uuid.UUID            `json:"attempt_id"`
	SessionID  string               `json:"session_id"`
	OnBehalfOf *audit.ActProvenance `json:"on_behalf_of"`
}

// ledgerEvent is one audit.events row.
type ledgerEvent struct {
	EventID   string
	ActorKind int16
	ActorID   uuid.UUID
	Extra     string
}

// accountableOperatorID derives the operator ID of email through the git
// config identity source, the derivation an operator's own commands record.
func accountableOperatorID(t *testing.T, email string) uuid.UUID {
	t.Helper()
	path := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(path, []byte("[user]\n\temail = "+email+"\n"), 0o600); err != nil {
		t.Fatalf("write the git config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", path)
	principal, err := cli.GitConfigOperatorSource{}.Resolve(t.Context())
	if err != nil {
		t.Fatalf("derive the operator ID of %s: %v", email, err)
	}
	return principal.ID
}

// runOpsDBSQL runs the audited command tree with args, the real SQL outbox,
// and the identity source the server's root command uses.
func runOpsDBSQL(t *testing.T, cfg *config.Config, pool *pgxpool.Pool, args ...string) (string, error) {
	t.Helper()
	factory := cli.System(cfg)
	var output bytes.Buffer
	factory.Out = &output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	root := searchCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs(args)
	err := root.Execute()
	return output.String(), err
}

// ledgerNow reads the ledger's clock, which stamps public.ops_outbox rows.
func ledgerNow(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(t.Context(), `SELECT now()`).Scan(&now); err != nil {
		t.Fatalf("read the ledger clock: %v", err)
	}
	return now
}

// decodeAttributionExtra decodes an event's extra payload.
func decodeAttributionExtra(t *testing.T, raw []byte) attributionExtra {
	t.Helper()
	var extra attributionExtra
	if err := json.Unmarshal(raw, &extra); err != nil {
		t.Fatalf("decode extra %s: %v", raw, err)
	}
	return extra
}

// startAuditPipeline runs the real audit consumer and the real relay over the
// operator outbox against a topic of this test's own. Both stop when the test
// ends.
func startAuditPipeline(t *testing.T, brokers, ledgerDSN string, pool *pgxpool.Pool) {
	t.Helper()
	topic := "audit.attribution-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-attribution-test-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: attributionPollInterval, YugabyteDSN: ledgerDSN,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(brokers), Topic: topic, ClientID: "tack-attribution-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the relay producer: %v", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(pool), FoundationDB: nil,
		PollInterval: attributionPollInterval, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("start the relay: %v", err)
	}
	relay.Start(t.Context())
	t.Cleanup(func() { _ = relay.Close() })
}

// waitForLedgerEvents polls audit.events until every event ID is projected,
// and fails with the rows it observed when the deadline passes first.
func waitForLedgerEvents(t *testing.T, pool *pgxpool.Pool, eventIDs []string) []ledgerEvent {
	t.Helper()
	var observed []ledgerEvent
	var lastErr error
	projected := waitFor(t, attributionDeadline, func() bool {
		rows, err := pool.Query(t.Context(),
			`SELECT event_id::text, actor_kind, actor_id, extra::text FROM audit.events WHERE event_id::text = ANY($1::text[])`, eventIDs)
		if err != nil {
			lastErr = err
			return false
		}
		observed, lastErr = pgx.CollectRows(rows, pgx.RowToStructByPos[ledgerEvent])
		return lastErr == nil && len(observed) == len(eventIDs)
	})
	if !projected {
		t.Fatalf("audit.events contains %d of %d events after %s (last error %v): %+v",
			len(observed), len(eventIDs), attributionDeadline, lastErr, observed)
	}
	return observed
}
