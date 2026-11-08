package integration_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	readVerdictBundleRef = "audit/verify-bundle"
	readVerdictReportKey = "file_sha256_ok"
)

func TestAuditVerifyRecordsTheVerdictAfterTheReport(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	orgID := uuid.Must(uuid.NewV7())
	storeManifestRead(t, ledger, orgID)
	keyPath, keyID, private := writeVerdictSigningKey(t)
	bundle := t.TempDir()
	now := clock.Now()
	if _, err := audit.Export(t.Context(), ledger.reader, private, keyID, audit.QueryFilter{
		OrgID: orgID, Oldest: now.Add(-time.Hour), Latest: now.Add(time.Hour),
		Action: "", ActorID: uuid.Nil, EntityID: uuid.Nil, RequestID: "", TraceID: "", Limit: 0,
	}, bundle); err != nil {
		t.Fatalf("export the bundle of %s: %v", orgID, err)
	}
	verifyArgs := []string{"audit", "verify", "--bundle", bundle, "--pub", keyPath, "--signers", keyID}

	t.Run("a passing check", func(t *testing.T) {
		operatorID, run := runReadLifecycleCommand(t, ledger, verifyArgs...)
		if run.err != nil {
			t.Fatalf("audit verify failed: %v\n%s", run.err, run.output())
		}
		output := decodeReadLifecycleOutput(t, run)
		requireCompletedReadRow(t, awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditVerify),
			referenceTarget(readVerdictBundleRef), output.Printed, nil)
	})

	t.Run("a failed check prints the report and keeps the exit code", func(t *testing.T) {
		otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("generate the other key: %v", err)
		}
		rejectedArgs := []string{"audit", "verify", "--bundle", bundle, "--pub", keyPath, "--signers", audit.KeyIdentifier(otherPublic)}
		operatorID, run := runReadLifecycleCommand(t, ledger, rejectedArgs...)
		var exitErr *exec.ExitError
		if !errors.As(run.err, &exitErr) || exitErr.ExitCode() != 1 || !strings.Contains(run.stdout, readVerdictReportKey) {
			t.Fatalf("audit verify with a signer outside the set = %v, want exit code 1 with the report printed\n%s", run.err, run.output())
		}
		output := decodeReadLifecycleOutput(t, run)
		requireVerdictReadRow(t, awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditVerify),
			referenceTarget(readVerdictBundleRef), output.Printed, nil, "audit verify")
	})

	t.Run("a finalization failure suppresses the report", func(t *testing.T) {
		if _, err := ledger.admin.Exec(t.Context(), readTestSQL(t, "runtime_audit_public_finalize_denied.sql")); err != nil {
			t.Fatalf("configure the finalization-denied role: %v", err)
		}
		operatorID := uuid.Must(uuid.NewV7())
		denied := builtAuditCommandAs(t, ledgerLoginDSN(t, ledger, finalizeDeniedRole), ledger.cfg.AuditReaderDSN, append([]string{
			"--execute", "--operator-id", operatorID.String(), "--operator-email", "read-verdict@example.test", "--output", "json",
		}, verifyArgs...)...)
		if denied.err == nil || strings.Contains(denied.stdout, readVerdictReportKey) {
			t.Fatalf("audit verify with a denied finalization = %v, want an error and no report\n%s", denied.err, denied.output())
		}
		recoverSearchRead(t, ledger.admin, operatorID)
	})
}
