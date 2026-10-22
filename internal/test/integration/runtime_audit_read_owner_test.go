package integration_test

import (
	_ "embed"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/runtime_audit_read_grants.sql
var readGrantsStatement string

//go:embed testdata/runtime_audit_read_denied.sql
var readDeniedStatements string

// insufficientPrivilegeSQLState is the error code of a refused table write.
const insufficientPrivilegeSQLState = "42501"

// TestRuntimeAuditReadStaleOwner recovers an expired read in a second
// process and requires the first owner to lose renewal, finalization, and
// admission. It also checks admission retries and renewal by the owner.
func TestRuntimeAuditReadStaleOwner(t *testing.T) {
	for _, backend := range readBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			stale := newReadFixture(t)
			stale.begin(t, backend.store)
			retried, err := backend.store.BeginRead(t.Context(), stale.admission)
			if err != nil || retried.Generation != stale.completion.Token.Generation ||
				!retried.AdmissionDeadline.Equal(stale.completion.Token.AdmissionDeadline) {
				t.Fatalf("retry the admission = %+v, %v; want the saved token", retried, err)
			}
			other := stale.admission
			other.Token.Owner = audit.NewReadOwner()
			if _, err := backend.store.BeginRead(t.Context(), other); !errors.Is(err, audit.ErrReadOwnerLost) {
				t.Fatalf("admission by another owner = %v, want audit.ErrReadOwnerLost", err)
			}
			late := newReadFixture(t)
			late.begin(t, backend.store)
			backend.expire(t, stale.admission.Token.EventID, late.admission.Token.EventID)
			if _, err := backend.store.BeginRead(t.Context(), late.admission); !errors.Is(err, audit.ErrReadAdmissionExpired) {
				t.Fatalf("late admission retry = %v, want audit.ErrReadAdmissionExpired", err)
			}
			child := startReadChild(t, readChildActionRecover, backend.childEnv())
			child.signal(t)
			if recovered := child.result(t); recovered == "0" {
				t.Fatal("the child process did not recover the expired reads")
			}
			if err := backend.store.FinalizeRead(t.Context(), late.completion); !errors.Is(err, audit.ErrReadOwnerLost) {
				t.Fatalf("finalization of the late read = %v, want audit.ErrReadOwnerLost", err)
			}
			requireTerminal(t, backend, late.admission.Token.EventID, audit.OutcomeIncomplete)
			token := stale.completion.Token
			if err := backend.store.RenewRead(t.Context(), token); !errors.Is(err, audit.ErrReadOwnerLost) {
				t.Fatalf("stale renewal = %v, want audit.ErrReadOwnerLost", err)
			}
			if err := backend.store.FinalizeRead(t.Context(), stale.completion); !errors.Is(err, audit.ErrReadOwnerLost) {
				t.Fatalf("stale finalization = %v, want audit.ErrReadOwnerLost", err)
			}
			if _, err := backend.store.BeginRead(t.Context(), stale.admission); !errors.Is(err, audit.ErrReadOwnerLost) {
				t.Fatalf("admission after recovery = %v, want audit.ErrReadOwnerLost", err)
			}
			event := requireTerminal(t, backend, token.EventID, audit.OutcomeIncomplete)
			if event.Error == nil || event.Context.Read != nil || event.Context.CompletedAt.IsZero() {
				t.Fatalf("recovered event = %+v, want an error, a completion time, and no read evidence", event)
			}

			renewed := newReadFixture(t)
			renewed.begin(t, backend.store)
			if err := backend.store.RenewRead(t.Context(), renewed.completion.Token); err != nil {
				t.Fatalf("renew the active read: %v", err)
			}
			if err := backend.store.FinalizeRead(t.Context(), renewed.completion); err != nil {
				t.Fatalf("finalize the renewed read: %v", err)
			}
			requireTerminal(t, backend, renewed.admission.Token.EventID, audit.OutcomeCompleted)
		})
	}
}

// TestRuntimeAuditReadReceiptCleanup delivers a finalized read through the
// real relay, which marks its receipt published in the transaction that
// removes the outbox records, and runs receipt cleanup in a second process.
// Both stores keep an unpublished receipt and a receipt published inside the
// retention period. Only the YugabyteDB case proves deletion after
// the retention period: the test moves its publication times back, and the
// FoundationDB store reads the wall clock, which the test cannot advance by
// one hour. The test also checks the YugabyteDB lifecycle grants.
func TestRuntimeAuditReadReceiptCleanup(t *testing.T) {
	for _, backend := range readBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			fixture := newReadFixture(t)
			fixture.begin(t, backend.store)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			eventID := fixture.admission.Token.EventID
			wrong := audit.PublishedRead{EventID: eventID, Digest: strings.Repeat("0", 64)}
			if err := backend.store.AcknowledgePublished(t.Context(), []audit.PublishedRead{wrong}); err == nil {
				t.Fatal("acknowledging a different digest succeeded")
			}
			cleanupInChild(t, backend)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("cleanup deleted the unpublished receipt: %v", err)
			}
			requireTerminal(t, backend, eventID, audit.OutcomeCompleted)
			deliverReadThroughRelay(t, backend, eventID)
			// A published receipt ignores later acknowledgments. The different
			// digest succeeds here only after the relay marked the receipt
			// published.
			if err := backend.store.AcknowledgePublished(t.Context(), []audit.PublishedRead{wrong}); err != nil {
				t.Fatalf("the relay did not mark the receipt published: %v", err)
			}
			published := audit.PublishedRead{EventID: eventID, Digest: fixture.completion.Digest}
			if err := backend.store.AcknowledgePublished(t.Context(), []audit.PublishedRead{published}); err != nil {
				t.Fatalf("repeat the acknowledgment: %v", err)
			}
			cleanupInChild(t, backend)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("cleanup deleted the receipt before its retention: %v", err)
			}
			if backend.agePublished == nil {
				t.Logf("%s: this case does not prove deletion after the retention period", backend.name)
				return
			}
			backend.agePublished(t)
			cleanupInChild(t, backend)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); !errors.Is(err, audit.ErrReadOwnerLost) {
				t.Fatalf("finalization after cleanup = %v, want audit.ErrReadOwnerLost", err)
			}
		})
	}
	t.Run("grants", checkReadGrants)
}

// cleanupInChild runs receipt cleanup in every bucket in a second process.
func cleanupInChild(t *testing.T, backend *readBackend) {
	t.Helper()
	child := startReadChild(t, readChildActionCleanup, backend.childEnv())
	child.signal(t)
	child.result(t)
}

// checkReadGrants requires the ownership and privileges that migration 021
// leaves, and refuses direct table writes by the application role.
func checkReadGrants(t *testing.T) {
	dsn := testenv.Ledger(t)
	application := applicationRolePool(t, dsn)
	var ownerHasMembers, ownerCreatesInPublic, tableRights bool
	var executableFunctions, publicFunctions int
	var stateOwner string
	err := application.QueryRow(t.Context(), readGrantsStatement).Scan(&ownerHasMembers, &ownerCreatesInPublic,
		&executableFunctions, &publicFunctions, &stateOwner, &tableRights)
	if err != nil {
		t.Fatalf("read the lifecycle grants: %v", err)
	}
	if ownerHasMembers || ownerCreatesInPublic || stateOwner != "ops_outbox_owner" {
		t.Fatalf("ops_outbox_owner members = %t, CREATE on public = %t, ops_read_state owner = %q",
			ownerHasMembers, ownerCreatesInPublic, stateOwner)
	}
	if executableFunctions != 8 || publicFunctions != 0 || tableRights {
		t.Fatalf("functions executable by app_auth and tack_migrator = %d, by PUBLIC = %d, table rights = %t",
			executableFunctions, publicFunctions, tableRights)
	}
	for _, statement := range strings.Split(strings.TrimSpace(readDeniedStatements), ";\n") {
		_, err := application.Exec(t.Context(), statement)
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != insufficientPrivilegeSQLState {
			t.Fatalf("%s as app_auth = %v, want SQLSTATE %s", statement, err, insufficientPrivilegeSQLState)
		}
	}
}
