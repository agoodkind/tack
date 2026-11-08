package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

func TestRecordedReadFingerprintsTheExactPrintedBytes(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	storedEvent, _ := storeManifestRead(t, ledger, uuid.Must(uuid.NewV7()))
	for _, format := range []string{"json", "text"} {
		t.Run(format, func(t *testing.T) {
			operatorID := uuid.Must(uuid.NewV7())
			run := builtAuditCommand(t, ledger.cfg.AuditReaderDSN,
				"--execute", "--operator-id", operatorID.String(), "--operator-email", "fingerprint@example.test",
				"--output", format, "audit", "get", "--event-id", storedEvent.String())
			if run.err != nil || run.stdout == "" {
				t.Fatalf("audit get --output %s = %v with output %q, want printed output\n%s", format, run.err, run.stdout, run.output())
			}
			row := awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditRead)
			digest := sha256.Sum256([]byte(run.stdout))
			if row.Context.Read == nil || row.Context.Read.PayloadSHA256 != hex.EncodeToString(digest[:]) {
				t.Fatalf("stored fingerprint %+v does not match the SHA-256 %x of the printed bytes %q", row.Context.Read, digest, run.stdout)
			}
		})
	}
}
