package integration_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// TestAuditManifestPayloadVerification checks the public command and its audit evidence.
func TestAuditManifestPayloadVerification(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	orgID := uuid.Must(uuid.NewV7())
	eventID, parts := storeManifestRead(t, ledger, orgID)
	operator := manifestRunOptions{
		readerDSN: ledger.cfg.AuditReaderDSN, lifecycle: rolePool(t, testenv.Ledger(t), "audit_operator"), operatorID: uuid.Must(uuid.NewV7()),
	}
	run := runManifestParts(t, ledger, operator, manifestArgs(orgID.String(), eventID.String(), "--verify-payloads", "--execute")...)
	if run.err != nil {
		t.Fatalf("verify valid manifest: %+v", run)
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(run.stdout), &envelope); err != nil {
		t.Fatalf("decode command output: %v", err)
	}
	var report struct {
		Verification audit.ManifestVerification `json:"verification"`
	}
	if err := json.Unmarshal(envelope.Result, &report); err != nil {
		t.Fatalf("decode verification: %v", err)
	}
	verification := report.Verification
	if !verification.PayloadVerified || verification.DeclaredCount != readTargetCount || verification.IdentityCount != readTargetCount ||
		verification.PartCount != uint32(len(parts)) || verification.DeclaredParts != uint32(len(parts)) ||
		verification.DeclaredDigest == "" || verification.DeclaredDigest != verification.RecomputedDigest {
		t.Fatalf("verification = %+v", verification)
	}
	for _, part := range parts {
		if strings.Contains(run.stdout, string(part.Payload)) {
			t.Fatal("verification output contains a manifest payload")
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, envelope.Result); err != nil {
		t.Fatalf("compact command result: %v", err)
	}
	digest := sha256.Sum256(compact.Bytes())
	requireStoredAuditReads(t, ledger, operator.operatorID, []manifestReadRun{{
		orgInput: orgID.String(), eventInput: eventID.String(), targetURI: "tack:audit-event/" + eventID.String(),
		outcome: audit.OutcomeCompleted, errorCode: "", messageContains: "", forbidden: nil,
		payloadSHA256: hex.EncodeToString(digest[:]), partURIs: []string{"tack:audit-event/" + eventID.String()}, verifyPayloads: true,
	}})
	requireManifestRecordingRefusals(t, ledger, operator, orgID, eventID, "--verify-payloads")

	cases := []struct {
		name    string
		fixture string
		message string
	}{
		{name: "missing part", fixture: "manifest_test_delete_part.sql", message: "index order"},
		{name: "altered payload", fixture: "manifest_test_tamper_part.sql", message: "does not match its digest"},
		{name: "wrong count", fixture: "manifest_test_wrong_count.sql", message: "declared read identities"},
		{name: "wrong root", fixture: "manifest_test_wrong_root.sql", message: "manifest digest"},
		{name: "malformed evidence", fixture: "manifest_test_malformed_evidence.sql", message: "valid read evidence"},
		{name: "incomplete evidence", fixture: "manifest_test_invalid_read_evidence.sql", message: "invalid read evidence"},
		{name: "invalid response fingerprint", fixture: "manifest_test_invalid_payload_digest.sql", message: "invalid read evidence"},
		{name: "oversized payload", fixture: "manifest_test_oversized_part.sql", message: "exceeds the size limit"},
	}
	admin := ledgerAdminPool(t)
	failedRuns := make([]manifestReadRun, 0, len(cases)+1)
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			fixtureOrg := uuid.Must(uuid.NewV7())
			fixtureEvent, _ := storeManifestRead(t, ledger, fixtureOrg)
			if _, err := admin.Exec(t.Context(), readTestSQL(t, current.fixture), fixtureOrg, fixtureEvent); err != nil {
				t.Fatalf("alter synthetic fixture: %v", err)
			}
			result := runManifestParts(t, ledger, operator, manifestArgs(fixtureOrg.String(), fixtureEvent.String(), "--verify-payloads", "--execute")...)
			if result.err == nil || !strings.Contains(result.err.Error(), current.message) || result.stdout != "" {
				t.Fatalf("verify %s: %+v", current.name, result)
			}
			failedRuns = append(failedRuns, manifestReadRun{
				orgInput: fixtureOrg.String(), eventInput: fixtureEvent.String(), targetURI: "tack:audit-event/" + fixtureEvent.String(),
				outcome: audit.OutcomeFailed, errorCode: "read_failed", messageContains: current.message,
				forbidden: nil, payloadSHA256: "", partURIs: nil, verifyPayloads: true,
			})
		})
	}
	otherOrg := uuid.Must(uuid.NewV7())
	wrongOrg := runManifestParts(t, ledger, operator, manifestArgs(otherOrg.String(), eventID.String(), "--verify-payloads", "--execute")...)
	if wrongOrg.err == nil || wrongOrg.stdout != "" {
		t.Fatalf("verify wrong organization: %+v", wrongOrg)
	}
	failedRuns = append(failedRuns, manifestReadRun{
		orgInput: otherOrg.String(), eventInput: eventID.String(), targetURI: "tack:audit-event/" + eventID.String(),
		outcome: audit.OutcomeFailed, errorCode: "read_failed", messageContains: "not found",
		forbidden: nil, payloadSHA256: "", partURIs: nil, verifyPayloads: true,
	})
	failedRuns = append(failedRuns, requireInvalidManifestIdentities(t, ledger, admin, operator)...)
	failedRuns = append(failedRuns, requireHistoricalManifestAbsence(t, ledger, operator))
	failedRuns = append(failedRuns, requireManifestDeclarationCases(t, ledger, admin, operator)...)
	requireStoredAuditReads(t, ledger, operator.operatorID, append([]manifestReadRun{{
		orgInput: orgID.String(), eventInput: eventID.String(), targetURI: "tack:audit-event/" + eventID.String(),
		outcome: audit.OutcomeCompleted, errorCode: "", messageContains: "", forbidden: nil,
		payloadSHA256: hex.EncodeToString(digest[:]), partURIs: []string{"tack:audit-event/" + eventID.String()}, verifyPayloads: true,
	}}, failedRuns...))
}
