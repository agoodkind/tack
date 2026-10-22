package integration_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// TestRuntimeAuditUserProtectedValues creates a user with UserRepo.Create,
// relays its change event, and consumes it as audit_writer. It reads the
// protected email and display name as audit_redactor and requires the ledger
// row and verified export bundles to omit their raw values. It uses
// audit_reader for ledger reads and audit_redactor to redact both values,
// checks that the redacted rows do not contain payloads, and verifies the
// export again.
func TestRuntimeAuditUserProtectedValues(t *testing.T) {
	stack := startWriterRoleStack(t)
	oldest := clock.Now().UTC().Add(-time.Minute)
	holder := createSQLAuditUser(t, stack.admin, sqlAuditPrincipal())
	userEvent := onlyEvent(t, sqlOutboxEvents(t, stack.admin, holder.ID), audit.PhaseChange)
	startOutboxRelay(t, stack)

	record := requireUserChange(t, stack, userEvent, holder)
	redactor, redactorPool := openRedactor(t, stack)
	requireProtectedValues(t, redactorPool, map[uuid.UUID]string{
		record.Email.PIIRef: holder.Email, record.DisplayName.PIIRef: holder.DisplayName,
	})
	latest := clock.Now().UTC().Add(time.Minute)
	personal := []string{holder.Email, holder.DisplayName}
	requireExportWithout(t, stack, audit.SystemOrgID(), oldest, latest, personal)

	redaction, err := audit.RedactActorInOrg(t.Context(), stack.reader, redactor, audit.SystemOrgID(), holder.ID)
	refs := []uuid.UUID{record.Email.PIIRef, record.DisplayName.PIIRef}
	if err != nil || redaction.Redacted != int64(len(refs)) {
		t.Fatalf("RedactActorInOrg for holder.ID %s: redaction = %+v, err = %v; want redaction.Redacted = %d and err = nil", holder.ID, redaction, err, len(refs))
	}
	requireRedacted(t, redactorPool, refs)
	requireExportWithout(t, stack, audit.SystemOrgID(), oldest, latest, personal)
}

// TestRuntimeAuditProtectedReferenceMismatch requires the consumer to reject
// a delta reference without a protected value and a protected value without
// a delta reference. It checks each event's ID and protected value error in
// the dead-letter table and requires the ledger to omit both events.
func TestRuntimeAuditProtectedReferenceMismatch(t *testing.T) {
	stack := startWriterRoleStack(t)
	orgID := uuid.Must(uuid.NewV7())
	subjectID := uuid.Must(uuid.NewV7())
	target := audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "", URI: "tack:user/" + subjectID.String(),
		RelationType: "", ID: subjectID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
	referenced := protectedChangeEvent(t, orgID, target, uuid.Must(uuid.NewV7()))
	unreferenced := protectedChangeEvent(t, orgID, target, uuid.Must(uuid.NewV7()))
	activeValue, err := json.Marshal(map[string]bool{"is_active": true})
	if err != nil {
		t.Fatalf("json.Marshal of synthetic user value: error = %v, want nil", err)
	}
	protectedText, err := json.Marshal("synthetic value")
	if err != nil {
		t.Fatalf("json.Marshal of synthetic protected value: error = %v, want nil", err)
	}
	unreferenced.Delta.After = activeValue
	unreferenced.Protected = []audit.ProtectedValue{{Ref: uuid.Must(uuid.NewV7()), SubjectID: subjectID, Value: protectedText}}
	stack.record(t, referenced, unreferenced)

	errorTexts := stack.waitForDeadLetters(t, 2)
	for _, event := range []audit.Event{referenced, unreferenced} {
		rejected := slices.ContainsFunc(errorTexts, func(text string) bool {
			return strings.Contains(text, event.EventID.String()) && strings.Contains(text, "protected")
		})
		if !rejected {
			t.Fatalf("errorTexts = %q, want an entry containing event.EventID %s and \"protected\"", errorTexts, event.EventID)
		}
		if _, err := stack.reader.GetByID(t.Context(), event.EventID); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("GetByID for rejected event %s: error = %v, want pgx.ErrNoRows", event.EventID, err)
		}
	}
}

// protectedChangeEvent builds a completed format 2 change event with one
// email reference in Delta.After. It does not set Protected.
func protectedChangeEvent(t *testing.T, orgID uuid.UUID, target audit.Target, ref uuid.UUID) audit.Event {
	t.Helper()
	after, err := json.Marshal(map[string]audit.ProtectedReference{"email": {PIIRef: ref}})
	if err != nil {
		t.Fatalf("json.Marshal of synthetic user value: error = %v, want nil", err)
	}
	event := completeEvent(orgID, userActor(), target, audit.OperationDatabaseChange, audit.PhaseChange, audit.OutcomeCompleted)
	event.Delta = &audit.Delta{Before: nil, After: after, Changed: nil}
	return event
}
