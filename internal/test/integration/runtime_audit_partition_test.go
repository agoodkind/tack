package integration_test

import (
	_ "embed"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_action_count.sql
var runtimeAuditActionCountQuery string

//go:embed testdata/runtime_audit_notarization_count.sql
var runtimeAuditNotarizationCountQuery string

// TestRuntimeAuditPartitionMaintenance verifies that maintenance and notarization do not create compliance events.
func TestRuntimeAuditPartitionMaintenance(t *testing.T) {
	started := clock.Now().UTC().Add(-time.Minute)
	adminDSN, brokers := testenv.Ledger(t), testenv.Kafka(t)
	admin := ledgerAdminPool(t)
	topic := "audit.partition-test-" + uuid.NewString()[:8]
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{
			brokers,
		},
		Topic:          topic,
		ClientID:       "tack-partition-test",
		ProduceTimeout: 30 * time.Second,
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
	consumer, err := audit.NewConsumer(
		t.Context(),
		audit.ConsumerConfig{
			Brokers: []string{
				brokers,
			},
			Topic:           topic,
			GroupID:         "tack-partition-test-" + uuid.NewString()[:8],
			BatchSize:       32,
			PollInterval:    100 * time.Millisecond,
			YugabyteDSN:     adminDSN,
			SigningKeyPath:  keyPath,
			NotarizerPeriod: time.Second,
			SigningHost:     service,
		},
	)
	if err != nil {
		t.Fatalf("creating the audit consumer failed: %v", err)
	}

	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	waitForCount(t, admin, runtimeAuditNotarizationCountQuery, service)
	if rows := countMatches(
		t,
		admin,
		runtimeAuditActionCountQuery,
		string(audit.VerbAuditPartitionMaintain),
		started,
	); rows != 0 {
		t.Fatalf("maintenance recorded %d compliance events", rows)
	}

	notarized := countMatches(
		t,
		admin,
		runtimeAuditActionCountQuery,
		string(audit.VerbAuditNotarized),
		started,
	)
	if notarized != 0 {
		t.Fatalf(
			"the ledger has %d audit.notarized events after the notarizer ran; the notarizer must not record events",
			notarized,
		)
	}
}

func waitForCount(t *testing.T, pool *pgxpool.Pool, query string, argument string) {
	t.Helper()
	deadline := clock.Now().Add(runtimeAuditDeadline)

	for countMatches(t, pool, query, argument) == 0 {
		if clock.Now().After(deadline) {
			t.Fatalf(
				"the query did not count any matching rows for %s within %s",
				argument,
				runtimeAuditDeadline,
			)
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
