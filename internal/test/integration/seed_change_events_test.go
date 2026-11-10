package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

const (
	// seedOutboxReadLimit bounds each outbox read of the seed change event test.
	seedOutboxReadLimit = 10000
	// seedTransactionTimeout limits each FoundationDB transaction that reads
	// the seed's outbox.
	seedTransactionTimeout = 5 * time.Second
)

// TestSeedRecordsChangeEventsOnAnEmptyLedger migrates an empty ledger
// database, starts a fresh FoundationDB cluster, and runs the built `seed`
// command without --allow-reseed. Every change event that the seed writes to
// the YugabyteDB and FoundationDB outboxes must use the operation ID of the
// seed command intent and the operator as its actor. Both outboxes must
// contain at least one change event, and the FoundationDB outbox must
// contain a node.create event.
func TestSeedRecordsChangeEventsOnAnEmptyLedger(t *testing.T) {
	if len(fdbadapter.TestPrefixRange()) != 0 {
		t.Fatal("a FoundationDB test prefix is active; this test reads the fresh cluster without a prefix")
	}
	dsn := emptyLedgerDatabase(t)
	if err := migration.Migrate(t.Context(), dsn, migrations.FS); err != nil {
		t.Fatalf("migrate the empty ledger database: %v", err)
	}
	clusterFile := testenv.FreshFoundationDB(t)
	operatorID := uuid.Must(uuid.NewV7())
	runSeedCommand(t, buildMigrateServer(t), dsn, clusterFile, operatorID)

	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	sqlEvents := seedSQLOutboxEvents(t, pool)
	operationID := seedOperationID(t, sqlEvents)
	requireSeedChanges(t, "YugabyteDB", sqlEvents, operationID, operatorID)
	fdbEvents := seedFoundationDBOutboxEvents(t, clusterFile)
	requireSeedChanges(t, "FoundationDB", fdbEvents, operationID, operatorID)
	for _, event := range fdbEvents {
		if event.Verb == string(audit.VerbNodeCreate) {
			return
		}
	}
	t.Fatalf("the FoundationDB outbox does not contain a node.create event: %+v", fdbEvents)
}

// runSeedCommand runs `seed --execute` from binary against dsn and
// clusterFile. It discards standard output, because the seed prints bearer
// values there, and reports standard error when the command fails.
func runSeedCommand(t *testing.T, binary, dsn, clusterFile string, operatorID uuid.UUID) {
	t.Helper()
	suffix := uuid.NewString()[:8]
	logDir := t.TempDir()
	process := exec.CommandContext(t.Context(), binary, "--execute", "--operator-id", operatorID.String(),
		"--operator-email", "operator@example.com", "seed")
	process.Env = append(os.Environ(),
		"DATABASE_URL="+dsn, "AUDIT_OPERATOR_DSN="+dsn, "FDB_CLUSTER_FILE="+clusterFile,
		"SEED_EMAIL=seed-audit-"+suffix+"@example.test", "SEED_NAME=Seed Audit Test", "SEED_ORG_SLUG=seed-audit-"+suffix,
		"LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+filepath.Join(logDir, "tack.json"),
		"LOG_TEXT_FILE="+filepath.Join(logDir, "tack.log"))
	stderr := &bytes.Buffer{}
	process.Stdout, process.Stderr = nil, stderr
	if err := process.Run(); err != nil {
		t.Fatalf("seed: %v\n%s", err, stderr.String())
	}
}

// seedSQLOutboxEvents returns the plain events in the YugabyteDB outbox.
func seedSQLOutboxEvents(t *testing.T, pool *pgxpool.Pool) []audit.Event {
	t.Helper()
	rows, err := audit.NewPoolOutbox(pool).ReadBatch(t.Context(), seedOutboxReadLimit)
	if err != nil {
		t.Fatalf("read the YugabyteDB outbox: %v", err)
	}
	events := make([]audit.Event, 0, len(rows))
	for _, row := range rows {
		if len(row.Messages) == 0 {
			events = append(events, row.Event)
		}
	}
	return events
}

// seedFoundationDBOutboxEvents returns the plain events in the FoundationDB
// outbox of clusterFile.
func seedFoundationDBOutboxEvents(t *testing.T, clusterFile string) []audit.Event {
	t.Helper()
	stores, err := fdbadapter.NewStores(clusterFile, seedTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("open the stores: %v", err)
	}
	entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), nil, seedOutboxReadLimit)
	if err != nil {
		t.Fatalf("read the FoundationDB outbox: %v", err)
	}
	events := make([]audit.Event, 0, len(entries))
	for _, entry := range entries {
		var event audit.Event
		if json.Unmarshal(entry.Event, &event) == nil && event.Verb != "" {
			events = append(events, event)
		}
	}
	return events
}

// seedOperationID returns the operation ID in the extra field of the one
// pending bootstrap.seed intent in events.
func seedOperationID(t *testing.T, events []audit.Event) uuid.UUID {
	t.Helper()
	var intents []audit.Event
	for _, event := range events {
		if event.Verb == string(audit.VerbBootstrapSeed) && event.Outcome == audit.OutcomePending {
			intents = append(intents, event)
		}
	}
	if len(intents) != 1 {
		t.Fatalf("the YugabyteDB outbox has %d seed intents, want 1", len(intents))
	}
	var extra struct {
		OpID uuid.UUID `json:"op_id"`
	}
	if err := json.Unmarshal(intents[0].Extra, &extra); err != nil || extra.OpID == uuid.Nil {
		t.Fatalf("seed intent %s extra = %s, %v, want an operation ID", intents[0].EventID, intents[0].Extra, err)
	}
	return extra.OpID
}

// requireSeedChanges requires at least one change event in events and
// requires every change event to be valid with operationID and actorID.
func requireSeedChanges(t *testing.T, store string, events []audit.Event, operationID, actorID uuid.UUID) {
	t.Helper()
	changes := 0
	for _, event := range events {
		if event.Context.Phase != audit.PhaseChange {
			continue
		}
		changes++
		if err := audit.ValidateEvent(event); err != nil {
			t.Fatalf("%s change event %s is invalid: %v", store, event.EventID, err)
		}
		if event.Context.OperationID != operationID || event.Actor.ID != actorID {
			t.Fatalf("%s change event %s operation %s actor %s, want operation %s actor %s",
				store, event.EventID, event.Context.OperationID, event.Actor.ID, operationID, actorID)
		}
	}
	if changes == 0 {
		t.Fatalf("the %s outbox does not contain a change event of the seed", store)
	}
}
