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
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

// ledgerVerbsDeadline bounds the wait for the consumer to project the
// recorded ledger verb events.
const ledgerVerbsDeadline = 90 * time.Second

// recordedLedgerVerbs are the four host-only ledger verbs. The test records
// one row of each.
var recordedLedgerVerbs = []audit.Verb{
	audit.VerbOpsLedgerNodePrepare, audit.VerbOpsLedgerNodeWait,
	audit.VerbOpsLedgerBootstrapWait, audit.VerbOpsLedgerAuditBootstrap,
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

// TestDatagenSeedReportsHostOnlyLedgerVerbs runs `ops qa datagen seed
// --commit` through the audited command tree before any ledger verb row
// exists and requires all four host-only ledger verbs listed with zero rows.
// It then records one system-organization event per verb through the
// production Kafka recorder and the real audit consumer, seeds again, and
// requires each verb with at least one row and a latest event time.
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
	for verb, rows := range ledgerVerbRows(t, runDatagenSeedCommit(t, cfg, admin)) {
		if rows != 0 {
			t.Fatalf("verb %s rows = %d before any row was recorded, want 0", verb, rows)
		}
	}
	recordLedgerVerbEvents(t, cfg)
	waitForLedgerVerbRows(t, cfg)
	for verb, rows := range ledgerVerbRows(t, runDatagenSeedCommit(t, cfg, admin)) {
		if rows < 1 {
			t.Fatalf("verb %s rows = %d after one row was recorded, want at least 1", verb, rows)
		}
	}
}

// ledgerVerbRows requires the seed result to list the four host-only ledger
// verbs, each with a latest event time when it has rows, and returns the row
// count of each.
func ledgerVerbRows(t *testing.T, output datagenSeedLedgerVerbsOutput) map[string]int64 {
	t.Helper()
	rows := map[string]int64{}
	for _, verb := range output.Result.LedgerVerbs.Verbs {
		rows[verb.Verb] = verb.Rows
		if verb.Rows > 0 && verb.LatestEventAt == "" {
			t.Fatalf("verb %s has %d rows and no latest event time", verb.Verb, verb.Rows)
		}
	}
	for _, verb := range recordedLedgerVerbs {
		if _, listed := rows[string(verb)]; !listed || len(rows) != len(recordedLedgerVerbs) {
			t.Fatalf("ledger verbs = %+v, want the four host-only ledger verbs", output.Result.LedgerVerbs.Verbs)
		}
	}
	return rows
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
