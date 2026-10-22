package integration_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/user"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

const (
	// drillLedgerContainer is the fixed container `ops backup yb-archive-node`
	// reads the tablet files from.
	drillLedgerContainer = "tack-yugabyte-1"
	// drillLedgerDatabase is the database the testenv ledger creates.
	drillLedgerDatabase = "tack"
	// drillLedgerRocksDBDir is the tablet data directory under the base
	// directory that the testenv ledger and the drill's scratch engine share.
	drillLedgerRocksDBDir = "/home/yugabyte/var/data/yb-data/tserver/data/rocksdb"
	// drillMemberRole is the member role of org_members.
	drillMemberRole = 15
	// drillPollInterval is how often the relay, the consumer, and the test poll.
	drillPollInterval = 200 * time.Millisecond
	// drillLedgerRowDeadline bounds the wait for the relay and the consumer to
	// project the seeded event into audit.events.
	drillLedgerRowDeadline = 90 * time.Second
)

// TestRestoreDrillRestoresLedgerWithSchemaGuard exports a ledger migrated
// through the schema guard migration, archives its tablets, and runs the
// restore drill. The YugabyteDB leg applies the exported schema to a scratch
// engine, restores the snapshot, and verifies the auth rows and the audit
// chain.
func TestRestoreDrillRestoresLedgerWithSchemaGuard(t *testing.T) {
	ctx := t.Context()
	node := testenv.StartEmptyLedger(t, drillLedgerContainer)
	if err := postgres.Migrate(ctx, node.DSN, migrations.FS); err != nil {
		t.Fatalf("migrate the empty ledger: %v", err)
	}
	cfg := restoreDrillConfig(t, node)
	if err := ops.RunAuditSeedRoles(ctx, cfg); err != nil {
		t.Fatalf("seed-roles: %v", err)
	}
	admin, err := pgxpool.New(ctx, node.DSN)
	if err != nil {
		t.Fatalf("open the ledger pool: %v", err)
	}
	t.Cleanup(admin.Close)
	seedDrillAuthRows(t, admin)
	seedDrillLedgerRow(t, node.DSN, admin)

	if err := ops.RunBackupYBSnapshotExport(ctx, cfg); err != nil {
		t.Fatalf("export the ledger snapshot: %v", err)
	}
	if err := ops.RunBackupYBArchiveNode(ctx, cfg, ""); err != nil {
		t.Fatalf("archive the node's tablets: %v", err)
	}
	if err := ops.RunBackupRestoreDrill(ctx, cfg, ops.RestoreDrillOptions{YBRunKey: "", FDBTargetTime: nil}); err != nil {
		t.Fatalf("restore drill: %v", err)
	}
}

// restoreDrillConfig points the export, the node archive, and the drill at
// the test ledger, a new bucket, and a backup root the Docker daemon sees.
func restoreDrillConfig(t *testing.T, node testenv.EmptyLedgerNode) *config.Config {
	t.Helper()
	overlay, err := filepath.Abs(filepath.Join("..", "..", "..", "yugabyte-overlay", "yugabyted"))
	if err != nil {
		t.Fatalf("resolve the yugabyted overlay: %v", err)
	}
	bucket := testenv.ObjectStore(t)
	cfg := &config.Config{
		DatabaseURL: node.DSN, YugabyteDB: drillLedgerDatabase,
		BackupRoot:              filepath.Join(testenv.SharedDir(t), "backups"),
		BackupYBImage:           node.Image,
		BackupYBOverlayPath:     overlay,
		BackupYBRocksDBDir:      drillLedgerRocksDBDir,
		BackupFDBNetwork:        node.Network,
		BackupFDBContinuous:     false,
		BackupYBMasterAddresses: node.MasterAddress,
		BackupS3Endpoint:        bucket.Endpoint,
		BackupS3Region:          bucket.Region,
		BackupS3BucketMain:      bucket.Bucket,
		BackupS3AccessKey:       bucket.AccessKey,
		BackupS3SecretKey:       bucket.SecretKey,
	}
	logins := []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
	}
	for _, login := range logins {
		*login = uuid.NewString()
	}
	return cfg
}

// seedDrillAuthRows writes one row into each auth table the drill counts.
func seedDrillAuthRows(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	holder, err := postgres.NewUserRepo(admin).Create(ctx, &user.User{
		ID: uuid.Must(uuid.NewV7()), Email: "drill-" + uuid.NewString() + "@example.invalid",
		DisplayName: "Drill Holder", AvatarURL: nil, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("create the drill user: %v", err)
	}
	if _, err := postgres.NewTokenRepo(admin).Create(ctx, holder.ID, uuid.NewString(), "restore drill"); err != nil {
		t.Fatalf("create the drill token: %v", err)
	}
	member := &org.Member{
		ID: uuid.Nil, OrgID: uuid.Must(uuid.NewV7()), UserID: holder.ID, Role: drillMemberRole, CreatedAt: now,
	}
	if err := postgres.NewOrgMemberRepo(admin).AddMember(ctx, member); err != nil {
		t.Fatalf("add the drill org member: %v", err)
	}
}

// seedDrillLedgerRow writes one event to the operator outbox and waits until
// the relay and the consumer project it into audit.events.
func seedDrillLedgerRow(t *testing.T, ledgerDSN string, admin *pgxpool.Pool) {
	t.Helper()
	ctx := t.Context()
	startDrillAuditPipeline(t, ledgerDSN, admin)
	orgID := uuid.Must(uuid.NewV7())
	event := audit.Event{
		Verb:       string(audit.VerbNodeRead),
		EventID:    uuid.Must(uuid.NewV7()),
		Actor:      audit.Actor{Type: audit.ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:     audit.Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context:    audit.EventContext{OrgID: orgID, Source: audit.SourceMCP},
		Outcome:    audit.OutcomeOK,
		OccurredAt: time.Now().UTC(),
	}
	if err := audit.NewPoolOutbox(admin).WriteOutbox(ctx, event); err != nil {
		t.Fatalf("write the drill event to the outbox: %v", err)
	}
	deadline := time.Now().Add(drillLedgerRowDeadline)
	for {
		var projected int
		err := admin.QueryRow(ctx, `SELECT count(*) FROM audit.events WHERE org_id = $1`, orgID).Scan(&projected)
		if err == nil && projected == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("audit.events contains %d rows of org %s after %s (last error %v), want 1",
				projected, orgID, drillLedgerRowDeadline, err)
		}
		time.Sleep(drillPollInterval)
	}
}

// startDrillAuditPipeline runs the audit consumer and the relay over the
// operator outbox against a topic of this test's own. Both stop when the test
// ends.
func startDrillAuditPipeline(t *testing.T, ledgerDSN string, admin *pgxpool.Pool) {
	t.Helper()
	brokers := testenv.Kafka(t)
	topic := "audit.restore-drill-test-" + uuid.NewString()[:8]
	consumer, err := audit.NewConsumer(t.Context(), audit.ConsumerConfig{
		Brokers: []string{brokers}, Topic: topic, GroupID: "tack-restore-drill-test-" + uuid.NewString()[:8],
		BatchSize: 32, PollInterval: drillPollInterval, YugabyteDSN: ledgerDSN,
	})
	if err != nil {
		t.Fatalf("start the audit consumer: %v", err)
	}
	consumer.Start(t.Context())
	t.Cleanup(func() { _ = consumer.Close() })
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(brokers), Topic: topic, ClientID: "tack-restore-drill-test",
		ProduceTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("open the relay producer: %v", err)
	}
	relay, err := audit.NewRelay(audit.RelayConfig{
		Recorder: recorder, Yugabyte: audit.NewPoolOutbox(admin), FoundationDB: nil,
		PollInterval: drillPollInterval, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("start the relay: %v", err)
	}
	relay.Start(t.Context())
	t.Cleanup(func() { _ = relay.Close() })
}
