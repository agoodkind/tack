package integration_test

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

const (
	// nodeAuditDeadline limits each wait for the relay and consumer.
	nodeAuditDeadline = 90 * time.Second
	// nodeAuditPollInterval sets the polling interval for the relay and
	// consumer.
	nodeAuditPollInterval = 100 * time.Millisecond
	// nodeAuditTransactionTimeout limits the duration of each FoundationDB
	// transaction that the test and relay run.
	nodeAuditTransactionTimeout = 10 * time.Second
)

// nodeAuditLedger provides a test audit pipeline with the production relay,
// both operator outboxes, a dedicated Kafka topic, and the audit consumer.
// Its ledger login inherits only audit_reader.
type nodeAuditLedger struct {
	cfg    *config.Config
	admin  *pgxpool.Pool
	reader *audit.Reader
}

// startNodeAuditLedger starts the audit consumer and the production relay
// from auditrelay.Open. Both stop when the test ends.
func startNodeAuditLedger(t *testing.T) *nodeAuditLedger {
	t.Helper()
	ledgerDSN := testenv.Ledger(t)
	t.Setenv("DATABASE_URL", ledgerDSN)
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	brokers := testenv.Kafka(t)
	admin, err := pgxpool.New(t.Context(), ledgerDSN)
	if err != nil {
		t.Fatalf("open the ledger admin pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.AuditReaderDSN = nodeAuditReaderDSN(t, admin, ledgerDSN)
	cfg.AuditKafkaBrokers = brokers
	cfg.AuditKafkaTopic = "audit.node-integrity-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: cfg.AuditKafkaTopic, GroupID: "tack-node-integrity-test-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: nodeAuditPollInterval, YugabyteDSN: ledgerDSN,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{brokers}, Topic: cfg.AuditKafkaTopic, YugabyteDSN: ledgerDSN,
		FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: nodeAuditTransactionTimeout,
		PollInterval: nodeAuditPollInterval, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("open the audit relay: %v", err)
	}
	opened.Relay.Start(t.Context())
	t.Cleanup(func() {
		_ = opened.Relay.Close()
		opened.Pool.Close()
	})
	reader, err := audit.NewReader(t.Context(), cfg.AuditReaderDSN)
	if err != nil {
		t.Fatalf("open the ledger reader: %v", err)
	}
	t.Cleanup(reader.Close)
	return &nodeAuditLedger{cfg: cfg, admin: admin, reader: reader}
}

// nodeAuditReaderDSN creates a LOGIN role that inherits only audit_reader. It
// returns the admin DSN with connection credentials for that role.
func nodeAuditReaderDSN(t *testing.T, admin *pgxpool.Pool, adminDSN string) string {
	t.Helper()
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("create the role secret: %v", err)
	}
	login := "tack_test_node_audit_" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
	password := hex.EncodeToString(secret)
	statement := "CREATE ROLE " + login + " LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD '" + password + "' IN ROLE audit_reader"
	if _, err := admin.Exec(t.Context(), statement); err != nil {
		t.Fatalf("create %s: %v", login, err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(t.Context(), "DROP ROLE IF EXISTS "+login) })
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the admin DSN: %v", err)
	}
	parsed.User = url.UserPassword(login, password)
	return parsed.String()
}

// entityRows reads ledger rows for entityID through Reader.QueryPage until
// want rows match verb. The audit query command uses the same read operation.
func (l *nodeAuditLedger) entityRows(t *testing.T, orgID, entityID uuid.UUID, verb audit.Verb, want int) []audit.Row {
	t.Helper()
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		page, err := l.reader.QueryPage(t.Context(), audit.QueryFilter{
			OrgID: orgID, Oldest: clock.Now().Add(-time.Hour), Latest: clock.Now().Add(time.Hour),
			Action: string(verb), ActorID: uuid.Nil, EntityID: entityID, RequestID: "", TraceID: "", Limit: 0,
		}, "")
		if err == nil && len(page.Rows) >= want {
			return page.Rows
		}
		if clock.Now().After(deadline) {
			t.Fatalf("ledger rows of %s for %s after %s = %+v (error %v), want %d", verb, entityID, nodeAuditDeadline, page.Rows, err, want)
		}
		time.Sleep(nodeAuditPollInterval)
	}
}
