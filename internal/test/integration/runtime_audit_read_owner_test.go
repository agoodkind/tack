package integration_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// insufficientPrivilegeSQLState is the error code YugabyteDB returns when it denies a table write.
const insufficientPrivilegeSQLState = "42501"

// TestRuntimeAuditReadStaleOwner recovers an expired read in a second operating system
// process and requires RenewRead, FinalizeRead, and BeginRead to reject the stale owner.
// It also checks admission retries and renewal by the active owner.
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
				t.Fatal("recover expired reads in the child process: recovered 0 reads, want at least 1")
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
				t.Fatalf("recovered event = %+v, want an error and a completion time without read evidence", event)
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

// TestRuntimeAuditReadReceiptCleanup checks repeated acknowledgments and receipt cleanup
// for each store. Cleanup runs in a second operating system process before and after the
// real relay delivers a finalized read. The test requires cleanup to retain unpublished
// receipts and receipts published less than one hour ago.
//
// The relay records publication and deletes delivered outbox records in one transaction.
// Only the YugabyteDB case verifies deletion after the one-hour retention period. The test
// moves stored publication times back with administrator UPDATE statements. Deletion also
// proves that the relay marked the receipt published, because the test does not
// acknowledge that read directly.
//
// FoundationDB uses the wall clock, and the test cannot wait one hour to verify deletion.
//
// The test also checks the YugabyteDB lifecycle grants.
func TestRuntimeAuditReadReceiptCleanup(t *testing.T) {
	for _, backend := range readBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			checkRepeatedAcknowledgment(t, backend)
			fixture := newReadFixture(t)
			fixture.begin(t, backend.store)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			eventID := fixture.admission.Token.EventID
			wrong := audit.PublishedRead{EventID: eventID, Digest: strings.Repeat("0", 64)}
			if err := backend.store.AcknowledgePublished(t.Context(), []audit.PublishedRead{wrong}); err == nil {
				t.Fatal("acknowledge a different digest: got success, want an error")
			}
			cleanupInChild(t, backend)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("finalize with the unpublished receipt after cleanup: %v", err)
			}
			requireTerminal(t, backend, eventID, audit.OutcomeCompleted)
			deliverReadThroughRelay(t, backend, eventID)
			cleanupInChild(t, backend)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("finalize before receipt retention expires: %v", err)
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

// checkRepeatedAcknowledgment requires two acknowledgments with the receipt digest to
// succeed after finalization. It then requires an acknowledgment with a different digest
// to fail.
func checkRepeatedAcknowledgment(t *testing.T, backend *readBackend) {
	t.Helper()
	fixture := newReadFixture(t)
	fixture.begin(t, backend.store)
	if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
		t.Fatalf("finalize the read before acknowledgment: %v", err)
	}
	eventID := fixture.admission.Token.EventID
	published := audit.PublishedRead{EventID: eventID, Digest: fixture.completion.Digest}
	for attempt := range 2 {
		if err := backend.store.AcknowledgePublished(t.Context(), []audit.PublishedRead{published}); err != nil {
			t.Fatalf("acknowledge the read with the receipt digest on attempt %d: %v", attempt+1, err)
		}
	}
	wrong := audit.PublishedRead{EventID: eventID, Digest: strings.Repeat("0", 64)}
	if err := backend.store.AcknowledgePublished(t.Context(), []audit.PublishedRead{wrong}); err == nil {
		t.Fatal("got success for acknowledgment with a different digest after publication, want an error")
	}
}

// cleanupInChild runs receipt cleanup in every bucket in a second process.
func cleanupInChild(t *testing.T, backend *readBackend) {
	t.Helper()
	child := startReadChild(t, readChildActionCleanup, backend.childEnv())
	child.signal(t)
	child.result(t)
}

// checkReadGrants checks the ownership and privileges from migration 021.
// It requires YugabyteDB to reject direct table writes by the application role.
func checkReadGrants(t *testing.T) {
	dsn := testenv.Ledger(t)
	application := applicationRolePool(t, dsn)
	var ownerHasMembers, ownerCreatesInPublic, tableRights bool
	var executableFunctions, publicFunctions int
	var stateOwner string
	err := application.QueryRow(t.Context(), readTestSQL(t, "runtime_audit_read_grants.sql")).Scan(&ownerHasMembers, &ownerCreatesInPublic,
		&executableFunctions, &publicFunctions, &stateOwner, &tableRights)
	if err != nil {
		t.Fatalf("read the lifecycle grants: %v", err)
	}
	if ownerHasMembers || ownerCreatesInPublic || stateOwner != "ops_outbox_owner" {
		t.Fatalf("ops_outbox_owner members = %t, CREATE on public = %t, ops_read_state owner = %q, want false, false, and ops_outbox_owner",
			ownerHasMembers, ownerCreatesInPublic, stateOwner)
	}
	if executableFunctions != 8 || publicFunctions != 0 || tableRights {
		t.Fatalf("functions executable by app_auth and tack_migrator = %d, by PUBLIC = %d, table rights = %t, want 8, 0, and false",
			executableFunctions, publicFunctions, tableRights)
	}
	denied := readTestSQL(t, "runtime_audit_read_denied.sql")
	for _, statement := range strings.Split(strings.TrimSpace(denied), ";\n") {
		_, err := application.Exec(t.Context(), statement)
		var postgresError *pgconn.PgError
		if !errors.As(err, &postgresError) || postgresError.Code != insufficientPrivilegeSQLState {
			t.Fatalf("%s as app_auth = %v, want SQLSTATE %s", statement, err, insufficientPrivilegeSQLState)
		}
	}
}
