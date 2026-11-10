package integration

import (
	_ "embed"
	"net"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

const (
	// clusterAuditBootstrapRows is the intent and outcome of the first
	// audit-bootstrap and of the refused second one.
	clusterAuditBootstrapRows = 4
	// clusterBootstrapWaitRows is the read row of each bootstrap-wait that
	// ran after the outbox existed: the red and green 2 and 2 waits, the red
	// 3, 3, 3 wait, the 3 and 3 wait, and the green 3, 3, 3 wait.
	clusterBootstrapWaitRows = 5
)

//go:embed testdata/ledger_bootstrap_manifest_count.sql
var clusterManifestCountQuery string

// requireServedByYB1 requires a connection through pool to be served by the
// node at yb1Address, read with inet_server_addr().
func requireServedByYB1(t *testing.T, pool *pgxpool.Pool, yb1Address string) {
	t.Helper()
	var served string
	if err := pool.QueryRow(t.Context(), clusterServingAddressQuery).Scan(&served); err != nil {
		t.Fatalf("read the serving node address: %v", err)
	}

	if !net.ParseIP(served).Equal(net.ParseIP(yb1Address)) {
		t.Fatalf("the keyword DSN connection is served by %s, want yb1 at %s", served, yb1Address)
	}

	t.Logf("the keyword DSN connection is served by yb1 at %s (inet_server_addr)", served)
}

// logTabletCount logs the number of tack database tablets on the node that
// serves pool, read from yb_local_tablets. A failed read fails the test and
// lets the remaining steps run.
func logTabletCount(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var tablets int64
	if err := pool.QueryRow(t.Context(), clusterTabletCountQuery).Scan(&tablets); err != nil {
		t.Errorf("read the ledger tablet count with %q: %v", clusterTabletCountQuery, err)
		return
	}

	t.Logf(
		"ledger tablet count after the migrations: %d (yb_local_tablets, namespace tack)",
		tablets,
	)
}

// requireClusterOutboxRows requires the outbox rows of both commands before
// the relay drains them.
func requireClusterOutboxRows(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	bootstraps := clusterOutboxEvents(t, admin, audit.VerbOpsLedgerAuditBootstrap)
	if len(bootstraps) != clusterAuditBootstrapRows {
		t.Fatalf(
			"outbox audit-bootstrap rows = %d, want %d",
			len(bootstraps),
			clusterAuditBootstrapRows,
		)
	}

	var manifests int
	if err := admin.QueryRow(t.Context(), clusterManifestCountQuery).Scan(&manifests); err != nil {
		t.Fatalf("count the read manifests in the outbox: %v", err)
	}

	if manifests != clusterBootstrapWaitRows {
		t.Fatalf(
			"outbox read manifests = %d, want %d bootstrap-wait reads",
			manifests,
			clusterBootstrapWaitRows,
		)
	}
}

// requireClusterLedgerRows waits for the relay, Kafka, and the consumer to
// project both commands' rows into audit.events and checks their outcomes.
func requireClusterLedgerRows(t *testing.T, admin *pgxpool.Pool) {
	t.Helper()
	verbs := []string{
		string(audit.VerbOpsLedgerAuditBootstrap),
		string(audit.VerbOpsLedgerBootstrapWait),
	}
	want := clusterAuditBootstrapRows + clusterBootstrapWaitRows
	var observed []bootstrapLedgerRow
	var lastErr error
	projected := waitFor(
		t,
		bootstrapRowsDeadline,
		func() bool {
			rows, err := admin.Query(t.Context(), bootstrapRowsQuery, verbs)
			if err != nil {
				lastErr = err
				return false
			}

			observed, lastErr = pgx.CollectRows(rows, pgx.RowToStructByPos[bootstrapLedgerRow])

			return lastErr == nil && len(observed) >= want
		},
	)
	if !projected || len(observed) != want {
		t.Fatalf(
			"audit.events contains %d rows after %s, want %d (last error %v): %+v",
			len(observed),
			bootstrapRowsDeadline,
			want,
			lastErr,
			observed,
		)
	}

	var bootstrapOutcomes []string

	for _, row := range bootstrapRowsOfVerb(observed, audit.VerbOpsLedgerAuditBootstrap) {
		requireBootstrapOpID(t, audit.VerbOpsLedgerAuditBootstrap, row)
		bootstrapOutcomes = append(bootstrapOutcomes, row.Outcome)
	}

	slices.Sort(bootstrapOutcomes)
	wantOutcomes := []string{
		string(audit.OutcomeError),
		string(audit.OutcomeOK),
		string(audit.OutcomePending),
		string(audit.OutcomePending),
	}
	if !slices.Equal(bootstrapOutcomes, wantOutcomes) {
		t.Fatalf("audit-bootstrap outcomes = %v, want %v", bootstrapOutcomes, wantOutcomes)
	}

	var waitOutcomes []string

	for _, row := range bootstrapRowsOfVerb(observed, audit.VerbOpsLedgerBootstrapWait) {
		requireBootstrapOpID(t, audit.VerbOpsLedgerBootstrapWait, row)
		waitOutcomes = append(waitOutcomes, row.Outcome)
	}

	slices.Sort(waitOutcomes)
	wantWaitOutcomes := []string{
		string(audit.OutcomeCompleted),
		string(audit.OutcomeCompleted),
		string(audit.OutcomeCompleted),
		string(audit.OutcomeFailed),
		string(audit.OutcomeFailed),
	}
	if !slices.Equal(waitOutcomes, wantWaitOutcomes) {
		t.Fatalf("bootstrap-wait outcomes = %v, want %v", waitOutcomes, wantWaitOutcomes)
	}
}
