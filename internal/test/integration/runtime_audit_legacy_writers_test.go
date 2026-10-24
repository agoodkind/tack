package integration_test

import (
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"
	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// legacyDeadLetterTopic, legacyDeadLetterPartition, and
	// legacyDeadLetterOffset name the Kafka record that the legacy consumer
	// dead-lettered.
	legacyDeadLetterTopic     = "audit.events.v1"
	legacyDeadLetterPartition = 7
	legacyDeadLetterOffset    = 4242
	// legacyDeadLetterError is the failure text of the legacy dead letter.
	legacyDeadLetterError = `insert audit event: ERROR: no partition of relation "events" found for row (SQLSTATE 23514)`
)

// TestRuntimeAuditLegacyOutboxRecords writes the public.ops_outbox rows with
// the legacy insert statement and the FoundationDB operator outbox entries
// with the legacy versionstamped key. The current relay must publish every
// record and empty both outboxes, and the current consumer must store one
// ledger row per event.
func TestRuntimeAuditLegacyOutboxRecords(t *testing.T) {
	for _, revision := range legacyRevisions {
		t.Run(revision, func(t *testing.T) {
			sqlRows := legacyFixtures(t, revision, "ops_outbox")
			entries := legacyFixtures(t, revision, "fdb_outbox")
			clusterFile := testenv.FoundationDB(t)
			identity := uuid.New()
			prefix := append([]byte("tack-test:"), identity[:]...)
			fdbadapter.SetTestPrefix(prefix)
			t.Cleanup(func() { clearReadPrefix(t, clusterFile, prefix) })
			admin := ledgerAdminPool(t)
			for _, row := range sqlRows {
				if _, err := admin.Exec(t.Context(), readTestSQL(t, "legacy_audit/ops_outbox_insert.sql"), row.EventID, []byte(row.Value)); err != nil {
					t.Fatalf("Inserting legacy outbox row %s failed: %v", row.EventID, err)
				}
			}
			writeLegacyEntries(t, clusterFile, prefix, entries)

			pipeline := newPartsPipeline(t)
			pipeline.startConsumer(t)
			startLegacyRelay(t, pipeline, clusterFile)
			waitFor(t, "deletion of the legacy outbox rows", func() bool {
				for _, row := range sqlRows {
					if pipeline.count(t, outboxRowsQuery, row.EventID) != 0 {
						return false
					}
				}
				return true
			})
			stores, err := fdbadapter.NewStores(clusterFile, 5*time.Second, nil)
			if err != nil {
				t.Fatalf("Opening the FoundationDB stores failed: %v", err)
			}
			waitFor(t, "clearing of the legacy FoundationDB outbox entries", func() bool {
				remaining, readErr := stores.OpsOutbox.ReadOutboxFrom(t.Context(), nil, 100)
				if readErr != nil {
					t.Fatalf("Reading the FoundationDB outbox failed: %v", readErr)
				}
				return len(remaining) == 0
			})
			requireLegacyLedger(t, pipeline, append(sqlRows, entries...))
		})
	}
}

// startLegacyRelay starts the production relay for both operator outboxes on
// the pipeline topic. Test cleanup closes the relay and its pool.
func startLegacyRelay(t *testing.T, pipeline partsPipeline, clusterFile string) {
	t.Helper()
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{pipeline.brokers}, Topic: pipeline.topic, YugabyteDSN: pipeline.ledger,
		FDBClusterFile: clusterFile, FDBTransactionTimeout: 5 * time.Second,
		PollInterval: 100 * time.Millisecond, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("Opening the relay failed: %v", err)
	}
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() { _ = opened.Relay.Close() })
	opened.Relay.Start(t.Context())
}

// writeLegacyEntries writes each fixture value under the key that the legacy
// FoundationDB writers packed: the ops_outbox family and an incomplete
// versionstamp with user version 0, one transaction per entry.
func writeLegacyEntries(t *testing.T, clusterFile string, prefix []byte, entries []legacyFixture) {
	t.Helper()
	database, err := fdbadapter.Open(clusterFile, 5*time.Second)
	if err != nil {
		t.Fatalf("Opening FoundationDB failed: %v", err)
	}
	for _, entry := range entries {
		key, err := tuple.Tuple{"ops_outbox", tuple.IncompleteVersionstamp(0)}.PackWithVersionstamp(prefix)
		if err != nil {
			t.Fatalf("Packing the legacy outbox key failed: %v", err)
		}
		if _, err := database.Transact(func(transaction fdb.Transaction) (any, error) {
			transaction.SetVersionstampedKey(fdb.Key(key), []byte(entry.Value))
			return nil, nil
		}); err != nil {
			t.Fatalf("Writing legacy outbox entry %s failed: %v", entry.EventID, err)
		}
	}
}

// TestRuntimeAuditLegacyDeadLetterReplay writes a dead-letter row with the
// legacy consumer insert statement and a payload from the legacy encoder. The
// `ops audit dlq replay` command must produce the payload to the current
// consumer, which must store one ledger row and delete the dead-letter row.
func TestRuntimeAuditLegacyDeadLetterReplay(t *testing.T) {
	for index, revision := range legacyRevisions {
		t.Run(revision, func(t *testing.T) {
			letter := legacyFixtures(t, revision, "dlq")[0]
			admin := ledgerAdminPool(t)
			offset := int64(legacyDeadLetterOffset + index)
			if _, err := admin.Exec(t.Context(), readTestSQL(t, "legacy_audit/dead_letter_insert.sql"),
				legacyDeadLetterTopic, int32(legacyDeadLetterPartition), offset, []byte(letter.Value), legacyDeadLetterError); err != nil {
				t.Fatalf("Inserting the legacy dead letter failed: %v", err)
			}
			pipeline := newPartsPipeline(t)
			pipeline.startConsumer(t)
			cfg := &config.Config{
				AuditReaderDSN: testenv.Ledger(t), AuditKafkaBrokers: pipeline.brokers, AuditKafkaTopic: pipeline.topic,
				AuditKafkaProduceTimeout: 30 * time.Second,
			}
			output, err := runOpsCommand(t, cfg, admin, "--execute", "--operator-id", uuid.Must(uuid.NewV7()).String(),
				"--operator-email", "operator@example.com", "--operator-name", "Legacy Replay Test",
				"ops", "audit", "dlq", "replay", "--limit", "100")
			if err != nil {
				t.Fatalf("ops audit dlq replay failed: %v\n%s", err, output)
			}
			requireLegacyLedger(t, pipeline, []legacyFixture{letter})
			waitFor(t, "deletion of the legacy dead letter", func() bool {
				return countRows(t, admin, readTestSQL(t, "legacy_audit/dead_letter_rows.sql"), letter.EventID) == 0
			})
		})
	}
}
