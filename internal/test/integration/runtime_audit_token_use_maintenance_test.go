package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"expvar"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// TestRuntimeAuditTokenUseAfterPartitionMaintenance starts the consumer as an
// audit_writer login with a pool of one ledger connection. The partition
// maintenance run at consumer startup uses that connection. After the run,
// the test records an auth.token_used event for a stored token. The consumer
// must store the event on the same connection and set the token's last use to
// the event time.
func TestRuntimeAuditTokenUseAfterPartitionMaintenance(t *testing.T) {
	admin := ledgerAdminPool(t)
	ledger := testenv.Ledger(t)
	parsed, err := url.Parse(roleLoginDSN(t, admin, ledger, "audit_writer"))
	if err != nil {
		t.Fatalf("Parsing the writer login address failed: %v", err)
	}
	query := parsed.Query()
	query.Set("pool_max_conns", "1")
	parsed.RawQuery = query.Encode()
	brokers := testenv.Kafka(t)
	topic := "audit.token-use-" + uuid.NewString()[:8]
	maintainedBefore := partitionMaintenanceRuns(t)
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-token-use-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: 100 * time.Millisecond, YugabyteDSN: parsed.String(),
	})
	if err != nil {
		t.Fatalf("Starting the audit consumer failed: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	waitFor(t, "the partition maintenance run at consumer startup", func() bool {
		return partitionMaintenanceRuns(t) > maintainedBefore
	})

	tokenID := insertTokenUseToken(t, admin)
	reader, err := audit.NewReader(t.Context(), ledger)
	if err != nil {
		t.Fatalf("Opening the audit reader failed: %v", err)
	}
	t.Cleanup(reader.Close)
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: []string{brokers}, Topic: topic, ClientID: "tack-token-use-test", ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Opening the audit producer failed: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	stack := runtimeAuditStack{topic: topic, admin: admin, reader: reader, recorder: recorder}
	usedAt := clock.Now().UTC().Truncate(time.Microsecond)
	event := audit.Event{
		Verb: string(audit.VerbAuthTokenUsed), EventID: uuid.Must(uuid.NewV7()),
		Actor:   audit.Actor{Type: audit.ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:  audit.Entity{Type: "auth", Name: "token_accepted"},
		Outcome: audit.OutcomeOK, OccurredAt: usedAt,
	}
	event.Context.OrgID = uuid.Must(uuid.NewV7())
	event.Context.APITokenID = tokenID
	stack.record(t, event)
	stack.waitForRow(t, event.EventID)
	var lastUsed *time.Time
	if err := admin.QueryRow(t.Context(), readTestSQL(t, "token_use_last_used.sql"), tokenID).Scan(&lastUsed); err != nil {
		t.Fatalf("Reading the last use of token %s failed: %v", tokenID, err)
	}
	if lastUsed == nil || !lastUsed.Equal(usedAt) {
		t.Fatalf("The last use of token %s is %v instead of the event time %s", tokenID, lastUsed, usedAt)
	}
}

// partitionMaintenanceRuns reads the count of successful partition
// maintenance runs in this process.
func partitionMaintenanceRuns(t *testing.T) int64 {
	t.Helper()
	runs, ok := expvar.Get("tack_audit_partition_maintenance_total").(*expvar.Map)
	if !ok {
		t.Fatalf("The partition maintenance counter is not published")
	}
	succeeded, ok := runs.Get("ok").(*expvar.Int)
	if !ok {
		return 0
	}
	return succeeded.Value()
}

// insertTokenUseToken stores one user and one API token for that user and
// returns the token ID. Test cleanup deletes the user and its token.
func insertTokenUseToken(t *testing.T, admin *pgxpool.Pool) uuid.UUID {
	t.Helper()
	hash := make([]byte, 32)
	if _, err := rand.Read(hash); err != nil {
		t.Fatalf("Creating the token hash failed: %v", err)
	}
	userID := uuid.Must(uuid.NewV7())
	tokenID := uuid.Must(uuid.NewV7())
	if _, err := admin.Exec(t.Context(), readTestSQL(t, "token_use_user_insert.sql"), userID,
		"token-use-"+userID.String()+"@example.com"); err != nil {
		t.Fatalf("Inserting user %s failed: %v", userID, err)
	}
	deleteUser := readTestSQL(t, "token_use_user_delete.sql")
	t.Cleanup(func() { _, _ = admin.Exec(context.WithoutCancel(t.Context()), deleteUser, userID) })
	if _, err := admin.Exec(t.Context(), readTestSQL(t, "token_use_token_insert.sql"), tokenID, userID,
		hex.EncodeToString(hash)); err != nil {
		t.Fatalf("Inserting token %s failed: %v", tokenID, err)
	}
	return tokenID
}
