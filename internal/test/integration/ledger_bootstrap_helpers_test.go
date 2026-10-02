package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
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
	"goodkind.io/tack/internal/testenv"
)

const (
	// bootstrapOperatorLogin is the operator login that ops provision seeds.
	bootstrapOperatorLogin = "tack_audit_operator"
	// bootstrapPollInterval is how often the relay and the consumer poll.
	bootstrapPollInterval = 100 * time.Millisecond
	// bootstrapProbeTimeout bounds the audit infrastructure probe.
	bootstrapProbeTimeout = 5 * time.Second
	// bootstrapInfrastructureQuery is the server root command's audit
	// infrastructure probe (cmd/server/commands.go).
	bootstrapInfrastructureQuery = "SELECT to_regclass('public.ops_outbox') IS NOT NULL, " +
		"EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tack_audit_operator')"
)

// bootstrapSecret returns a new random password for one seeded login.
func bootstrapSecret(t *testing.T) string {
	t.Helper()
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("generate a login secret: %v", err)
	}
	return hex.EncodeToString(secret)
}

// bootstrapConfig loads the server configuration against the empty node and
// points the provision and ledger node commands at it and at the test
// FoundationDB network.
func bootstrapConfig(t *testing.T, node testenv.EmptyLedgerNode) *config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", node.DSN)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	// Provision seeds each login with a value generated for this test only.
	logins := []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword,
	}
	for _, login := range logins {
		*login = bootstrapSecret(t)
	}
	cfg.BackupFDBNetwork = node.Network
	cfg.BackupFDBContinuous = false
	cfg.SeedEmail = ""
	cfg.SeedName = ""
	cfg.LedgerNodeHosts = bootstrapLedgerContainer + "=" + node.Address
	cfg.BackupYBMasterAddresses = node.MasterAddress
	return cfg
}

// bootstrapClusterDirectory copies the test FoundationDB cluster file into a
// directory that the Docker daemon sees at the same path, the directory the
// fdbcli one-shot mounts.
func bootstrapClusterDirectory(t *testing.T, clusterFile string) string {
	t.Helper()
	contents, err := os.ReadFile(clusterFile)
	if err != nil {
		t.Fatalf("read the FoundationDB cluster file %s: %v", clusterFile, err)
	}
	directory := testenv.SharedDir(t)
	copied := filepath.Join(directory, "fdb.cluster")
	if err := os.WriteFile(copied, contents, 0o600); err != nil {
		t.Fatalf("write the FoundationDB cluster file %s: %v", copied, err)
	}
	return directory
}

// openBootstrapPool opens a pool on dsn and closes it when the test ends. The
// pool connects on first use.
func openBootstrapPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open a ledger pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// operatorLoginDSN rewrites the superuser DSN to connect as the operator login.
func operatorLoginDSN(t *testing.T, adminDSN, password string) string {
	t.Helper()
	parsed, err := url.Parse(adminDSN)
	if err != nil {
		t.Fatalf("parse the ledger DSN: %v", err)
	}
	parsed.User = url.UserPassword(bootstrapOperatorLogin, password)
	return parsed.String()
}

// bootstrapInfrastructureProbe reads whether the operator outbox table and
// the operator login exist, through a superuser connection.
func bootstrapInfrastructureProbe(adminDSN string) audit.InfrastructureProbe {
	return func(ctx context.Context) (audit.InfrastructureState, error) {
		probeContext, cancel := context.WithTimeout(ctx, bootstrapProbeTimeout)
		defer cancel()
		connection, err := pgx.Connect(probeContext, adminDSN)
		if err != nil {
			return audit.InfrastructureState{}, fmt.Errorf("connect the audit infrastructure probe: %w", err)
		}
		defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
		var state audit.InfrastructureState
		row := connection.QueryRow(probeContext, bootstrapInfrastructureQuery)
		if err := row.Scan(&state.OutboxTableExists, &state.OperatorLoginExists); err != nil {
			return audit.InfrastructureState{}, fmt.Errorf("query the audit infrastructure probe: %w", err)
		}
		return state, nil
	}
}

// bootstrapCommandRunner returns a function that runs one audited ops command
// with the real operator outbox, the infrastructure probe, and the identity
// source the server's root command uses.
func bootstrapCommandRunner(t *testing.T, cfg *config.Config, operator *pgxpool.Pool) func(...string) (string, error) {
	t.Helper()
	outbox := audit.NewPoolOutbox(operator)
	probe := bootstrapInfrastructureProbe(cfg.DatabaseURL)
	return func(command ...string) (string, error) {
		factory := cli.System(cfg)
		var output bytes.Buffer
		factory.Out = &output
		factory.SetAuditOutbox(outbox)
		factory.SetAuditInfrastructureProbe(probe)
		root := searchCommandRoot(factory)
		root.SetContext(t.Context())
		global := []string{
			"--execute", "--output", "json", "--operator-id", bootstrapOperatorID,
			"--operator-email", "ledger-bootstrap@example.test", "--operator-name", "Ledger Bootstrap Test",
		}
		root.SetArgs(append(global, command...))
		err := root.Execute()
		return output.String(), err
	}
}

// startBootstrapAuditPipeline runs the real audit consumer and the real relay
// over the operator outbox against a topic of this test's own. Both stop when
// the test ends.
func startBootstrapAuditPipeline(t *testing.T, brokers, ledgerDSN string, admin *pgxpool.Pool) {
	t.Helper()
	topic := "audit.ledger-bootstrap-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-ledger-bootstrap-test-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: bootstrapPollInterval, YugabyteDSN: ledgerDSN,
		ClickHouseDSN: "", SigningKeyPath: "", NotarizerPeriod: 0, SigningHost: "",
		ReconcilePeriod: 0, ReconcileWindow: 0, LagWarnMessages: 0, SummaryEvery: 0,
		PartitionPeriod: 0, TopicRetention: 0,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(brokers), Topic: topic, ClientID: "tack-ledger-bootstrap-test",
		ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the relay producer: %v", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(admin), FoundationDB: nil,
		PollInterval: bootstrapPollInterval, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("start the relay: %v", err)
	}
	relay.Start(t.Context())
	t.Cleanup(func() { _ = relay.Close() })
}
