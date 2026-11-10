package integration_test

import (
	_ "embed"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_action_count.sql
var runtimeAuditActionCountQuery string

//go:embed testdata/runtime_audit_notarization_count.sql
var runtimeAuditNotarizationCountQuery string

// partitionRunEvents counts the intent and result events for a run and its step.
const partitionRunEvents = 4

type partitionStepEvidence struct {
	HeadroomAfter *int `json:"headroom_weeks_after"`
}

// TestRuntimeAuditPartitionMaintenance verifies that startup maintenance records
// a completed run and step under the service actor. It also checks that the
// notarizer writes a notarization without recording an audit.notarized event.
func TestRuntimeAuditPartitionMaintenance(t *testing.T) {
	started := clock.Now().UTC().Add(-time.Minute)
	adminDSN, brokers := testenv.Ledger(t), testenv.Kafka(t)
	admin := ledgerAdminPool(t)
	topic := "audit.partition-test-" + uuid.NewString()[:8]
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{brokers}, Topic: topic, ClientID: "tack-partition-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("opening the audit producer failed: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	keyPath := filepath.Join(t.TempDir(), "audit-signing.pem")
	if err := audit.GenerateAuditSigningKey(keyPath); err != nil {
		t.Fatalf("generating the signing key failed: %v", err)
	}
	service := "tack-audit-consumer-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-partition-test-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: adminDSN,
		SigningKeyPath: keyPath, NotarizerPeriod: time.Second, SigningHost: service,
	})
	if err != nil {
		t.Fatalf("creating the audit consumer failed: %v", err)
	}
	consumer.RecordPartitionRuns(audit.NewRecorderOutbox(recorder), audit.ServiceActor(cli.ServiceActorID(service), service))
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	reader, err := audit.NewReader(t.Context(), adminDSN)
	if err != nil {
		t.Fatalf("opening the audit reader failed: %v", err)
	}
	t.Cleanup(reader.Close)

	rows := waitForServiceRows(t, reader, cli.ServiceActorID(service), started)
	requirePartitionRun(t, rows)
	waitForCount(t, admin, runtimeAuditNotarizationCountQuery, service)
	notarized := countMatches(t, admin, runtimeAuditActionCountQuery, string(audit.VerbAuditNotarized), started)
	if notarized != 0 {
		t.Fatalf("the ledger has %d audit.notarized events after the notarizer ran; the notarizer must not record events", notarized)
	}
}

func waitForServiceRows(t *testing.T, reader *audit.Reader, actorID uuid.UUID, oldest time.Time) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for {
		var rows []audit.Row
		filter := audit.QueryFilter{
			OrgID: audit.SystemOrgID(), Oldest: oldest, Latest: clock.Now().UTC().Add(time.Minute),
			Action: string(audit.VerbAuditPartitionMaintain), ActorID: actorID, EntityID: uuid.Nil,
			RequestID: "", TraceID: "", Limit: partitionRunEvents * 2,
		}
		err := reader.StreamQuery(t.Context(), filter, func(row audit.Row) error {
			rows = append(rows, row)
			return nil
		})
		if err != nil {
			t.Fatalf("reading the partition maintenance rows failed: %v", err)
		}
		if len(rows) >= partitionRunEvents {
			return rows
		}
		if clock.Now().After(deadline) {
			t.Fatalf("the ledger has %d partition maintenance rows after %s; the test requires %d", len(rows), runtimeAuditDeadline, partitionRunEvents)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func requirePartitionRun(t *testing.T, rows []audit.Row) {
	t.Helper()
	var runs, steps int
	for _, row := range rows {
		if row.Context.Phase != audit.PhaseResult {
			continue
		}
		if row.Outcome != audit.OutcomeCompleted {
			t.Fatalf("partition maintenance row %s has outcome %s; the test requires a completed outcome", row.EventID, row.Outcome)
		}
		if row.Context.ParentOperationID == uuid.Nil {
			runs++
			continue
		}
		steps++
		var evidence partitionStepEvidence
		if err := json.Unmarshal(row.Extra, &evidence); err != nil || evidence.HeadroomAfter == nil {
			t.Fatalf("partition step result %s does not contain valid headroom after maintenance: %s (decoding returned %v)", row.EventID, row.Extra, err)
		}
	}
	if runs < 1 || steps < 1 {
		t.Fatalf("the ledger has %d run results and %d step results; the test requires at least one of each", runs, steps)
	}
}

func waitForCount(t *testing.T, pool *pgxpool.Pool, query string, argument string) {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)
	for countMatches(t, pool, query, argument) == 0 {
		if clock.Now().After(deadline) {
			t.Fatalf("the query did not count any matching rows for %s within %s", argument, runtimeAuditDeadline)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func countMatches(t *testing.T, pool *pgxpool.Pool, query string, arguments ...any) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(t.Context(), query, arguments...).Scan(&count); err != nil {
		t.Fatalf("counting rows failed: %v", err)
	}
	return count
}
