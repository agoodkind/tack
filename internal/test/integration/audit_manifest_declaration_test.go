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

// requireManifestDeclarationCases checks current and historical evidence classifications.
func requireManifestDeclarationCases(t *testing.T, ledger *nodeAuditLedger, admin *pgxpool.Pool, operator manifestRunOptions) []manifestReadRun {
	t.Helper()
	cases := []struct {
		name    string
		outcome audit.Outcome
		fixture string
		message string
		nonRead bool
	}{
		{name: "completed missing evidence", outcome: audit.OutcomeCompleted, fixture: "manifest_test_remove_read.sql", message: "invalid read evidence"},
		{name: "partially completed missing evidence", outcome: audit.OutcomePartiallyCompleted, fixture: "manifest_test_remove_read.sql", message: "invalid read evidence"},
		{name: "null evidence", outcome: audit.OutcomeCompleted, fixture: "manifest_test_null_read.sql", message: "valid read evidence"},
		{name: "empty evidence", outcome: audit.OutcomeCompleted, fixture: "manifest_test_empty_read.sql", message: "invalid read evidence"},
		{name: "zero-target read", outcome: audit.OutcomeCompleted, fixture: "", message: "did not record a manifest declaration"},
		{name: "failed read", outcome: audit.OutcomeFailed, fixture: "", message: "did not record read evidence"},
		{name: "incomplete read", outcome: audit.OutcomeIncomplete, fixture: "", message: "did not record read evidence"},
		{name: "completed change", outcome: audit.OutcomeCompleted, fixture: "", message: "did not record read evidence", nonRead: true},
	}
	runs := make([]manifestReadRun, 0, len(cases))
	for _, current := range cases {
		t.Run(current.name, func(t *testing.T) {
			orgID := uuid.Must(uuid.NewV7())
			event := completeEvent(orgID, userActor(), readNodeTarget(), audit.OperationRead, audit.PhaseRead, current.outcome)
			if current.nonRead {
				event.Context.OperationClass = audit.OperationDatabaseChange
				event.Context.Phase = audit.PhaseChange
				event.Delta = &audit.Delta{Before: nil, After: []byte(`{"name":"synthetic"}`), Changed: []string{"name"}}
			}
			if !current.nonRead && (current.outcome == audit.OutcomeCompleted || current.outcome == audit.OutcomePartiallyCompleted) {
				payloadDigest := sha256.Sum256([]byte("synthetic response"))
				event.Context.Read = &audit.ReadEvidence{Count: 0, PayloadSHA256: hex.EncodeToString(payloadDigest[:]), PayloadFormat: "application/json", ManifestSHA256: "", ManifestParts: 0}
			}
			if current.outcome != audit.OutcomeCompleted {
				event.Error = &audit.EventError{Code: "synthetic_failure", Message: "synthetic failure"}
			}
			if current.outcome == audit.OutcomePartiallyCompleted {
				event.Context.CommittedSteps = []string{"synthetic step"}
			}
			if err := audit.ValidateEvent(event); err != nil {
				t.Fatalf("validate synthetic event: %v", err)
			}
			if err := audit.NewPoolOutbox(ledger.admin).WriteOutbox(t.Context(), event); err != nil {
				t.Fatalf("write synthetic event: %v", err)
			}
			deadline := clock.Now().Add(nodeAuditDeadline)
			for {
				_, err := ledger.reader.GetByID(t.Context(), event.EventID)
				if err == nil {
					break
				}
				if clock.Now().After(deadline) {
					t.Fatalf("synthetic event did not appear: %v", err)
				}
				time.Sleep(nodeAuditPollInterval)
			}
			if current.fixture != "" {
				if _, err := admin.Exec(t.Context(), readTestSQL(t, current.fixture), orgID, event.EventID); err != nil {
					t.Fatalf("alter synthetic event: %v", err)
				}
			}
			run := runManifestParts(t, ledger, operator, manifestArgs(orgID.String(), event.EventID.String(), "--verify-payloads", "--execute")...)
			if run.err == nil || !strings.Contains(run.err.Error(), current.message) || run.stdout != "" {
				t.Fatalf("%s verification = %+v", current.name, run)
			}
			runs = append(runs, manifestReadRun{
				orgInput: orgID.String(), eventInput: event.EventID.String(), targetURI: "tack:audit-event/" + event.EventID.String(),
				outcome: audit.OutcomeFailed, errorCode: "read_failed", messageContains: current.message,
				forbidden: nil, payloadSHA256: "", partURIs: nil, verifyPayloads: true,
			})
		})
	}
	return runs
}
