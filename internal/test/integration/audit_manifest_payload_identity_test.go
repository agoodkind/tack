package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// requireInvalidManifestIdentities checks correctly hashed malformed targets.
func requireInvalidManifestIdentities(t *testing.T, ledger *nodeAuditLedger, admin *pgxpool.Pool, operator manifestRunOptions) []manifestReadRun {
	t.Helper()
	payloads := []string{`[{}]`, `[null]`, `[{"kind":"resource"}]`}
	runs := make([]manifestReadRun, 0, len(payloads))
	for _, payload := range payloads {
		orgID := uuid.Must(uuid.NewV7())
		eventID, _ := storeManifestRead(t, ledger, orgID)
		digest := sha256.Sum256([]byte(payload))
		hexDigest := hex.EncodeToString(digest[:])
		if _, err := admin.Exec(t.Context(), readTestSQL(t, "manifest_test_invalid_identity.sql"), orgID, eventID, []byte(payload), hexDigest); err != nil {
			t.Fatalf("write invalid synthetic identity: %v", err)
		}
		if _, err := admin.Exec(t.Context(), readTestSQL(t, "manifest_test_remove_extra_parts.sql"), orgID, eventID); err != nil {
			t.Fatalf("remove extra synthetic parts: %v", err)
		}
		if _, err := admin.Exec(t.Context(), readTestSQL(t, "manifest_test_single_identity.sql"), orgID, eventID, hexDigest); err != nil {
			t.Fatalf("write matching synthetic declaration: %v", err)
		}
		run := runManifestParts(t, ledger, operator, manifestArgs(orgID.String(), eventID.String(), "--verify-payloads", "--execute")...)
		if run.err == nil || !strings.Contains(run.err.Error(), "valid list of read targets") || run.stdout != "" || strings.Contains(run.err.Error(), payload) {
			t.Fatalf("invalid identity verification = %+v", run)
		}
		runs = append(runs, manifestReadRun{
			orgInput: orgID.String(), eventInput: eventID.String(), targetURI: "tack:audit-event/" + eventID.String(),
			outcome: audit.OutcomeFailed, errorCode: "read_failed", messageContains: "valid list of read targets",
			forbidden: []string{payload}, payloadSHA256: "", partURIs: nil, verifyPayloads: true,
		})
	}
	return runs
}

// requireHistoricalManifestAbsence checks an actual format-zero event without read evidence.
func requireHistoricalManifestAbsence(t *testing.T, ledger *nodeAuditLedger, operator manifestRunOptions) manifestReadRun {
	t.Helper()
	orgID := uuid.Must(uuid.NewV7())
	event := legacyReadEvent(orgID, audit.OutcomeOK)
	if err := audit.NewPoolOutbox(ledger.admin).WriteOutbox(t.Context(), event); err != nil {
		t.Fatalf("write historical synthetic event: %v", err)
	}
	deadline := clock.Now().Add(nodeAuditDeadline)
	for {
		_, err := ledger.reader.GetByID(t.Context(), event.EventID)
		if err == nil {
			break
		}
		if clock.Now().After(deadline) {
			t.Fatalf("historical event did not appear: %v", err)
		}
		time.Sleep(nodeAuditPollInterval)
	}
	run := runManifestParts(t, ledger, operator, manifestArgs(orgID.String(), event.EventID.String(), "--verify-payloads", "--execute")...)
	if run.err == nil || !strings.Contains(run.err.Error(), "did not record read evidence") || run.stdout != "" {
		t.Fatalf("historical event verification = %+v", run)
	}
	return manifestReadRun{
		orgInput: orgID.String(), eventInput: event.EventID.String(), targetURI: "tack:audit-event/" + event.EventID.String(),
		outcome: audit.OutcomeFailed, errorCode: "read_failed", messageContains: "did not record read evidence",
		forbidden: nil, payloadSHA256: "", partURIs: nil, verifyPayloads: true,
	}
}
