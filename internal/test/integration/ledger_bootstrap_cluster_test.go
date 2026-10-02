package integration

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

// clusterStep runs one ordered step and stops the test when the step fails.
// Every later step depends on the state the earlier steps leave.
func clusterStep(t *testing.T, name string, body func(t *testing.T)) {
	t.Helper()
	if !t.Run(name, body) {
		t.FailNow()
	}
}

// TestLedgerBootstrapCluster bootstraps a three-node ledger the way a ledger
// bootstrap run does, through the audited command tree and the real operator
// outbox. yb1 starts alone; ops ledger audit-bootstrap creates the outbox on
// it; yb2 and yb3 join yb1 one at a time behind ops ledger bootstrap-wait;
// the real relay, Kafka, and consumer then project every recorded row into
// audit.events. DATABASE_URL and the operator DSN list yb1, yb2, and yb3 in
// the QA keyword form, and TACK_BACKUP_YB_MASTER_ADDRESSES lists all three.
func TestLedgerBootstrapCluster(t *testing.T) {
	cluster := testenv.NewLedgerCluster(t, "yb1", "yb2", "yb3")
	cfg := clusterConfig(t, cluster)
	yb1 := cluster.Start(t, "yb1", "")
	admin := openBootstrapPool(t, cfg.DatabaseURL)
	operator := openBootstrapPool(t, cfg.AuditOperatorDSN)
	run := bootstrapCommandRunner(t, cfg, operator)
	wait := func(arguments ...string) (string, error) {
		return run(append([]string{"ops", "ledger", "bootstrap-wait"}, arguments...)...)
	}
	var version int64
	var secretHash string

	clusterStep(t, "1 bootstrap-wait before the outbox exists fails to record", func(t *testing.T) {
		output, err := wait("--masters", "1", "--tablet-servers", "1")
		requireCommandError(t, "bootstrap-wait on an empty ledger", err, output, "record read access")
	})
	clusterStep(t, "2 audit-bootstrap fails closed when DATABASE_URL cannot connect", func(t *testing.T) {
		closed := *cfg
		closed.DatabaseURL = clusterClosedDatabaseURL
		output, err := bootstrapCommandRunner(t, &closed, operator)("ops", "ledger", "audit-bootstrap")
		requireCommandError(t, "audit-bootstrap with a closed DATABASE_URL", err, output, "record command intent")
		if !ledgerIsEmpty(t, admin) {
			t.Fatal("yb1 has a migration table or an outbox table after a refused audit-bootstrap")
		}
	})
	clusterStep(t, "3 the keyword DSN is served by yb1", func(t *testing.T) {
		requireServedByYB1(t, admin, yb1.Address)
	})
	clusterStep(t, "4 audit-bootstrap migrates and records its intent and outcome", func(t *testing.T) {
		started := clock.Now()
		if output, err := run("ops", "ledger", "audit-bootstrap"); err != nil {
			t.Fatalf("audit-bootstrap on an empty ledger: %v\n%s", err, output)
		}
		logTiming(t, "audit_bootstrap_migrations_on_yb1", clock.Now().Sub(started))
		events := clusterOutboxEvents(t, admin, audit.VerbOpsLedgerAuditBootstrap)
		if len(events) != 2 {
			t.Fatalf("outbox audit-bootstrap rows = %d, want an intent and an outcome", len(events))
		}
		requireOutboxPair(t, events, audit.OutcomeOK)
		version, secretHash = migrationVersion(t, admin), operatorSecretHash(t, admin)
	})
	clusterStep(t, "5 a second audit-bootstrap is refused and recorded", func(t *testing.T) {
		output, err := run("ops", "ledger", "audit-bootstrap")
		requireCommandError(t, "a second audit-bootstrap", err, output, "already contains")
		events := clusterOutboxEvents(t, admin, audit.VerbOpsLedgerAuditBootstrap)
		if len(events) != 4 {
			t.Fatalf("outbox audit-bootstrap rows = %d, want 4", len(events))
		}
		if requireOutboxPair(t, events[2:], audit.OutcomeError) == outboxOpID(t, events[0]) {
			t.Fatal("the refused audit-bootstrap reused the first run's op id")
		}
		requireLedgerUnchanged(t, admin, version, secretHash)
	})
	clusterStep(t, "6 a wrong operator secret leaves the ledger unchanged", func(t *testing.T) {
		wrong := *cfg
		for _, login := range []*string{&wrong.AuditOperatorPassword} {
			*login = bootstrapSecret(t)
		}
		wrongOperator := openBootstrapPool(t, cluster.KeywordDSN(bootstrapOperatorLogin, wrong.AuditOperatorPassword))
		output, err := bootstrapCommandRunner(t, &wrong, wrongOperator)("ops", "ledger", "audit-bootstrap")
		if err == nil {
			t.Fatalf("audit-bootstrap with a wrong operator secret succeeded\n%s", output)
		}
		t.Logf("audit-bootstrap with a wrong operator secret failed as required: %v", err)
		requireLedgerUnchanged(t, admin, version, secretHash)
		requireOperatorLogin(t, cfg.AuditOperatorDSN)
	})
	clusterStep(t, "7 log the ledger tablet count", func(t *testing.T) {
		logTabletCount(t, admin)
	})
	clusterStep(t, "8 yb2 joins behind the 2 and 2 wait", func(t *testing.T) {
		output, err := wait("--masters", "2", "--tablet-servers", "2", "--deadline", "1s", "--poll", "1s")
		requireCommandError(t, "the 2 and 2 wait before yb2 starts", err, output, "tablet_servers=")
		started := clock.Now()
		cluster.Start(t, "yb2", "yb1")
		output, err = wait("--masters", "2", "--tablet-servers", "2")
		if err != nil {
			t.Fatalf("the 2 and 2 wait after yb2 starts: %v\n%s", err, output)
		}
		logTiming(t, "yb2_start_to_2_masters_2_tablet_servers", clock.Now().Sub(started))
		t.Logf("the 2 and 2 wait: %s", strings.TrimSpace(output))
	})
	clusterStep(t, "9 yb3 joins and the ledger re-replicates to 3", func(t *testing.T) {
		started := clock.Now()
		cluster.Start(t, "yb3", "yb1")
		// Mira M7 needed about 120 s after the third tablet server joined
		// before no tablet was under-replicated. A 5 s deadline fails.
		output, err := wait("--masters", "3", "--tablet-servers", "3", "--replicas", "3", "--deadline", "5s")
		requireCommandError(t, "the 3, 3, 3 wait right after yb3 starts", err, output, "elapsed")
		output, err = wait("--masters", "3", "--tablet-servers", "3")
		if err != nil {
			t.Fatalf("the 3 and 3 wait: %v\n%s", err, output)
		}
		logTiming(t, "yb3_start_to_3_masters_3_tablet_servers", clock.Now().Sub(started))
		masters, err := cluster.Admin(t.Context(), "list_all_masters")
		t.Logf("list_all_masters raw stdout (err %v):\n%s", err, masters)
		waitNumReplicas(t, cluster, 3)
		logTiming(t, "yb3_start_to_num_replicas_3", clock.Now().Sub(started))
		output, err = wait("--masters", "3", "--tablet-servers", "3", "--replicas", "3")
		if err != nil || !strings.Contains(output, "under_replicated_tablets=0") {
			t.Fatalf("the 3, 3, 3 wait: %v\n%s", err, output)
		}
		logTiming(t, "yb3_start_to_zero_under_replicated_tablets", clock.Now().Sub(started))
		t.Logf("the 3, 3, 3 wait: %s", strings.TrimSpace(output))
	})
	clusterStep(t, "10 every recorded row is in the outbox and then in audit.events", func(t *testing.T) {
		requireClusterOutboxRows(t, admin)
		startBootstrapAuditPipeline(t, testenv.Kafka(t), cfg.DatabaseURL, admin)
		requireClusterLedgerRows(t, admin)
	})
}

// requireLedgerUnchanged requires the migration version and the stored
// operator login hash to equal the values after the first audit-bootstrap.
func requireLedgerUnchanged(t *testing.T, admin *pgxpool.Pool, version int64, secretHash string) {
	t.Helper()
	if got := migrationVersion(t, admin); got != version {
		t.Fatalf("migration version = %d, want %d", got, version)
	}
	if operatorSecretHash(t, admin) != secretHash {
		t.Fatal("the stored operator login hash changed: seed-roles ran again")
	}
}
