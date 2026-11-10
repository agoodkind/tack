package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/testenv"
)

// assemblyDeniedRoles are the ledger roles other than audit_writer that
// application and operator processes use.
var assemblyDeniedRoles = []string{"app_auth", "audit_reader", "audit_operator", "audit_redactor"}

// TestRuntimeAuditAssemblyTablesConsumerOnly runs each statement in
// testdata/runtime_audit_assembly_denied.sql as each role in
// assemblyDeniedRoles. YugabyteDB must refuse every statement with SQLSTATE
// 42501, because migration 020 grants the transient assembly tables only to
// audit_writer, the role of the audit consumer.
func TestRuntimeAuditAssemblyTablesConsumerOnly(t *testing.T) {
	dsn := testenv.Ledger(t)
	statements := strings.Split(strings.TrimSpace(readTestSQL(t, "runtime_audit_assembly_denied.sql")), ";\n")
	for _, role := range assemblyDeniedRoles {
		pool := rolePool(t, dsn, role)
		for _, statement := range statements {
			_, err := pool.Exec(t.Context(), statement)
			var postgresError *pgconn.PgError
			if !errors.As(err, &postgresError) || postgresError.Code != insufficientPrivilegeState {
				t.Fatalf("%s as %s = %v, want SQLSTATE %s", statement, role, err, insufficientPrivilegeState)
			}
		}
	}
}
