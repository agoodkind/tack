package integration

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/internal/testenv/opsoutbox"
)

const (
	// clusterAdminLogin is the engine superuser every node starts with.
	clusterAdminLogin = "yugabyte"
	// clusterBackupMasters is the rendered three-node master list, which the
	// bootstrap wait must not use while yb3 is absent.
	clusterBackupMasters = "yb1:7100,yb2:7100,yb3:7100"
	// clusterEmptyQuery reads whether the migration table and the outbox
	// table are both absent.
	clusterEmptyQuery = "SELECT to_regclass('goose_db_version') IS NULL AND to_regclass('public.ops_outbox') IS NULL"
	// clusterVersionQuery reads the highest applied migration version.
	clusterVersionQuery = "SELECT max(version_id) FROM goose_db_version"
	// clusterSecretHashQuery reads the stored hash of the operator login. A
	// rotation stores a new salt and a new hash even for the same value.
	clusterSecretHashQuery = "SELECT rolpassword FROM pg_authid WHERE rolname = 'tack_audit_operator'"
	// clusterServingAddressQuery reads the address of the node that serves
	// the connection.
	clusterServingAddressQuery = "SELECT host(inet_server_addr())"
	// clusterTabletCountQuery counts the tablets of the tack database on the
	// node that serves the connection. On a one-node universe that node hosts
	// every tablet of the database.
	clusterTabletCountQuery = "SELECT count(*) FROM yb_local_tablets WHERE namespace_name = 'tack'"
	// clusterReplicaDeadline bounds the test's own wait for numReplicas.
	clusterReplicaDeadline = 10 * time.Minute
)

// clusterConfig loads the server configuration with DATABASE_URL and the
// operator DSN in the QA keyword form over the fixed addresses of yb1, yb2,
// and yb3. TACK_LEDGER_NODE_HOSTS pairs each node name with its fixed
// address, and the yb-admin one-shot joins the cluster network.
func clusterConfig(t *testing.T, cluster *testenv.LedgerCluster) *config.Config {
	t.Helper()
	t.Setenv("DATABASE_URL", cluster.KeywordDSN(clusterAdminLogin, cluster.AdminSecret()))
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	logins := []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword,
	}
	for _, login := range logins {
		*login = bootstrapSecret(t)
	}
	cfg.AuditOperatorDSN = cluster.KeywordDSN(bootstrapOperatorLogin, cfg.AuditOperatorPassword)
	cfg.BackupFDBNetwork = cluster.Network
	cfg.BackupYBImage = cluster.Image
	cfg.BackupYBMasterAddresses = clusterBackupMasters
	cfg.LedgerNodeHosts = clusterNodeHosts(cluster)
	cfg.LedgerTLSEnabled = false
	return cfg
}

// ledgerIsEmpty reads clusterEmptyQuery.
func ledgerIsEmpty(t *testing.T, pool *pgxpool.Pool) bool {
	t.Helper()
	var empty bool
	if err := pool.QueryRow(t.Context(), clusterEmptyQuery).Scan(&empty); err != nil {
		t.Fatalf("read whether the ledger is empty: %v", err)
	}
	return empty
}

// migrationVersion reads clusterVersionQuery.
func migrationVersion(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var version int64
	if err := pool.QueryRow(t.Context(), clusterVersionQuery).Scan(&version); err != nil {
		t.Fatalf("read the migration version: %v", err)
	}
	return version
}

// operatorSecretHash reads clusterSecretHashQuery.
func operatorSecretHash(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var hash string
	if err := pool.QueryRow(t.Context(), clusterSecretHashQuery).Scan(&hash); err != nil {
		t.Fatalf("read the operator login hash: %v", err)
	}
	return hash
}

// requireCommandError requires err to be non-nil and to contain want.
func requireCommandError(t *testing.T, label string, err error, output, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s = %v, want an error containing %q\n%s", label, err, want, output)
	}
	t.Logf("%s failed as required: %v", label, err)
}

// logTiming writes one timing line in the format the lead reads from CI.
func logTiming(t *testing.T, step string, elapsed time.Duration) {
	t.Helper()
	t.Logf("ledger bootstrap timing step=%s elapsed=%s", step, elapsed.Round(time.Millisecond))
}

// clusterOutboxEvents reads the outbox events of verb.
func clusterOutboxEvents(t *testing.T, admin *pgxpool.Pool, verb audit.Verb) []audit.Event {
	t.Helper()
	return opsoutbox.Events(t, admin, opsoutbox.Filter{Verb: verb})
}

// outboxOpID reads the op id from an outbox event's extra payload.
func outboxOpID(t *testing.T, event audit.Event) string {
	t.Helper()
	var extra struct {
		OpID string `json:"op_id"`
	}
	if err := json.Unmarshal(event.Extra, &extra); err != nil || extra.OpID == "" {
		t.Fatalf("outbox event %s extra %s has no op id: %v", event.EventID, event.Extra, err)
	}
	return extra.OpID
}

// requireOutboxPair requires two outbox events with one op id: a pending
// intent and an outcome of want. It returns the op id.
func requireOutboxPair(t *testing.T, pair []audit.Event, want audit.Outcome) string {
	t.Helper()
	outcomes := []string{string(pair[0].Outcome), string(pair[1].Outcome)}
	slices.Sort(outcomes)
	expected := []string{string(want), string(audit.OutcomePending)}
	slices.Sort(expected)
	if !slices.Equal(outcomes, expected) {
		t.Fatalf("outbox outcomes = %v, want %v", outcomes, expected)
	}
	opID := outboxOpID(t, pair[0])
	if outboxOpID(t, pair[1]) != opID {
		t.Fatalf("outbox op ids = %s and %s, want one op id", opID, outboxOpID(t, pair[1]))
	}
	return opID
}

// requireOperatorLogin connects and pings as the operator login with dsn.
func requireOperatorLogin(t *testing.T, dsn string) {
	t.Helper()
	connection, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect as the operator login: %v", err)
	}
	defer func() { _ = connection.Close(t.Context()) }()
	if err := connection.Ping(t.Context()); err != nil {
		t.Fatalf("ping as the operator login: %v", err)
	}
}

// waitNumReplicas reads get_universe_config in yb1 until numReplicas is want,
// and logs the raw output of the last read.
func waitNumReplicas(t *testing.T, cluster *testenv.LedgerCluster, want int) {
	t.Helper()
	var lastOutput string
	var lastErr error
	matched := waitFor(t, clusterReplicaDeadline, func() bool {
		lastOutput, lastErr = cluster.Admin(t.Context(), "get_universe_config")
		start, end := strings.Index(lastOutput, "{"), strings.LastIndex(lastOutput, "}")
		if lastErr != nil || start < 0 || end < start {
			return false
		}
		var universe struct {
			ReplicationInfo struct {
				LiveReplicas struct {
					NumReplicas int `json:"numReplicas"`
				} `json:"liveReplicas"`
			} `json:"replicationInfo"`
		}
		if json.Unmarshal([]byte(lastOutput[start:end+1]), &universe) != nil {
			return false
		}
		return universe.ReplicationInfo.LiveReplicas.NumReplicas == want
	})
	t.Logf("get_universe_config raw stdout:\n%s", lastOutput)
	if !matched {
		t.Fatalf("numReplicas did not become %d within %s (last error %v)", want, clusterReplicaDeadline, lastErr)
	}
}
