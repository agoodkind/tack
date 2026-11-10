package integration_test

import (
	_ "embed"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
)

//go:embed testdata/rollback_audit_outbox.sql
var rollbackAuditOutboxQuery string

// requireRollbackAudit requires one pending intent row and one outcome
// row with outcome for the rollback verb under operatorID.
func requireRollbackAudit(t *testing.T, fixture rolloutFixture, operatorID uuid.UUID, outcome audit.Outcome) {
	t.Helper()
	pool, err := pgxpool.New(t.Context(), fixture.cfg.DatabaseURL)
	if err != nil {
		t.Fatalf("cannot open outbox pool: %v", err)
	}
	t.Cleanup(pool.Close)
	rows, err := pool.Query(t.Context(), rollbackAuditOutboxQuery, string(audit.VerbOpsSearchAccessRollback), operatorID.String())
	if err != nil {
		t.Fatalf("cannot read operator outbox: %v", err)
	}
	recorded, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("cannot read operator outbox: %v", err)
	}
	outcomes := make(map[audit.Outcome]int)
	for _, value := range recorded {
		outcomes[audit.Outcome(value)]++
	}
	if outcomes[audit.OutcomePending] != 1 || outcomes[outcome] != 1 || len(outcomes) != 2 {
		t.Fatalf("rollback audit outcome counts %v must include one pending intent and one %s outcome", outcomes, outcome)
	}
}
