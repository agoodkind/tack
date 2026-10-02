package integration

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// ledgerVerbsDeadline bounds the wait for the consumer to project the
// recorded ledger verb events.
const ledgerVerbsDeadline = 90 * time.Second

// recordedLedgerVerbs are the host-only ledger verbs this test records once
// each. The test records no ops.ledger_audit_bootstrap event; the report
// must list that verb with zero rows.
var recordedLedgerVerbs = []audit.Verb{
	audit.VerbOpsLedgerNodePrepare, audit.VerbOpsLedgerNodeWait, audit.VerbOpsLedgerBootstrapWait,
}

type datagenSeedLedgerVerbsOutput struct {
	Result struct {
		LedgerVerbs struct {
			Verbs []struct {
				Verb          string `json:"verb"`
				Rows          int64  `json:"rows"`
				LatestEventAt string `json:"latest_event_time"`
			} `json:"verbs"`
		} `json:"ledger_verbs"`
	} `json:"result"`
}

// TestDatagenSeedReportsHostOnlyLedgerVerbs records one system-organization
// event for three host-only ledger verbs through the production Kafka
// recorder and the real audit consumer, then runs `ops qa datagen seed
// --commit` through the audited command tree. The seed result lists all four
// host-only ledger verbs: each recorded verb with at least one row and a
// latest event time, and ops.ledger_audit_bootstrap with zero rows.
func TestDatagenSeedReportsHostOnlyLedgerVerbs(t *testing.T) {
	ctx := t.Context()
	adminDSN := testenv.Ledger(t)
	t.Setenv("DATABASE_URL", adminDSN)
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	brokers := testenv.Kafka(t)
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatalf("admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.AuditReaderDSN = readerLoginDSN(t, admin, adminDSN)
	cfg.AuditKafkaBrokers = brokers
	cfg.AuditKafkaTopic = "audit.ledger-verbs-test-" + uuid.NewString()[:8]
	cfg.DatagenAllowTarget = "local"
	consumer, err := audit.NewConsumer(ctx, audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: cfg.AuditKafkaTopic, GroupID: "tack-ledger-verbs-test-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: adminDSN,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(ctx)
	t.Cleanup(func() { _ = consumer.Close() })
	recordLedgerVerbEvents(t, cfg)
	waitForLedgerVerbRows(t, cfg)

	output := runDatagenSeedCommit(t, cfg, admin)
	rows := map[string]int64{}
	for _, verb := range output.Result.LedgerVerbs.Verbs {
		rows[verb.Verb] = verb.Rows
		if verb.Rows > 0 && verb.LatestEventAt == "" {
			t.Fatalf("verb %s has %d rows and no latest event time", verb.Verb, verb.Rows)
		}
	}
	if len(output.Result.LedgerVerbs.Verbs) != 4 {
		t.Fatalf("ledger verbs = %+v, want the four host-only ledger verbs", output.Result.LedgerVerbs.Verbs)
	}
	for _, verb := range recordedLedgerVerbs {
		if rows[string(verb)] < 1 {
			t.Fatalf("verb %s rows = %d, want at least 1", verb, rows[string(verb)])
		}
	}
	if count, listed := rows[string(audit.VerbOpsLedgerAuditBootstrap)]; !listed || count != 0 {
		t.Fatalf("verb %s rows = %d (listed %t), want listed with 0", audit.VerbOpsLedgerAuditBootstrap, count, listed)
	}
}

// recordLedgerVerbEvents records one system-organization operator event per
// recorded ledger verb through the production Kafka recorder.
func recordLedgerVerbEvents(t *testing.T, cfg *config.Config) {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(cfg.AuditKafkaBrokers), Topic: cfg.AuditKafkaTopic,
		ClientID: "tack-ledger-verbs-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the audit producer: %v", err)
	}
	for _, verb := range recordedLedgerVerbs {
		event := audit.Event{
			Verb: string(verb), EventID: uuid.Must(uuid.NewV7()),
			Actor: audit.Actor{
				Type: audit.ActorOperator, ID: uuid.Must(uuid.NewV7()), Email: "", Name: "Ledger Verbs Test",
				SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
			},
			Entity: audit.Entity{Type: "system", NodeType: "", ID: audit.SystemOrgID(), Identifier: "", Name: ""},
			Context: audit.EventContext{
				OrgID: audit.SystemOrgID(), WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
				RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
			},
			Delta: nil, Outcome: audit.OutcomeOK, Error: nil, IdempotencyKey: "",
			OccurredAt: clock.Now().UTC(), Extra: nil,
		}
		if err := recorder.Record(t.Context(), event); err != nil {
			t.Fatalf("record a %s event: %v", verb, err)
		}
	}
	if err := recorder.CloseContext(t.Context()); err != nil {
		t.Fatalf("flush the audit producer: %v", err)
	}
}

// waitForLedgerVerbRows reads the ledger through the reader login until each
// recorded verb has a row on the system organization.
func waitForLedgerVerbRows(t *testing.T, cfg *config.Config) {
	t.Helper()
	reader, err := audit.NewReader(t.Context(), cfg.AuditReaderDSN)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	defer reader.Close()
	verbs := make([]string, 0, len(recordedLedgerVerbs))
	for _, verb := range recordedLedgerVerbs {
		verbs = append(verbs, string(verb))
	}
	deadline := clock.Now().Add(ledgerVerbsDeadline)
	for {
		presence, err := reader.VerbPresence(t.Context(), audit.SystemOrgID(), verbs)
		if err != nil {
			t.Fatalf("read the ledger verb presence: %v", err)
		}
		projected := 0
		for _, verb := range presence {
			if verb.Rows > 0 {
				projected++
			}
		}
		if projected == len(verbs) {
			return
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger verb presence after %s = %+v, want a row for each of %v", ledgerVerbsDeadline, presence, verbs)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// runDatagenSeedCommit runs `ops qa datagen seed --commit` once through the
// audited command tree with the real SQL outbox and decodes its result.
func runDatagenSeedCommit(t *testing.T, cfg *config.Config, pool *pgxpool.Pool) datagenSeedLedgerVerbsOutput {
	t.Helper()
	output := &bytes.Buffer{}
	factory := cli.System(cfg)
	factory.Out = output
	factory.SetAuditOutbox(audit.NewPoolOutbox(pool))
	t.Cleanup(factory.CloseAuditOutbox)
	root := searchCommandRoot(factory)
	root.SetContext(t.Context())
	root.SetArgs([]string{
		"--execute", "--output", "json", "--operator-id", "019dd226-440e-729a-a442-281aaf73ca32",
		"--operator-email", "operator@example.com", "--operator-name", "Ledger Verbs Test",
		"ops", "qa", "datagen", "seed", "--commit",
	})
	if err := root.Execute(); err != nil {
		t.Fatalf("ops qa datagen seed --commit: %v\n%s", err, output.String())
	}
	var decoded datagenSeedLedgerVerbsOutput
	if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
		t.Fatalf("decode the seed output %q: %v", output.String(), err)
	}
	return decoded
}
