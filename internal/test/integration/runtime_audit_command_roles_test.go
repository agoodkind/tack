package integration_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

var seededRoleBases = map[string][]string{
	"tack_audit_writer":   {"audit_writer"},
	"tack_audit_reader":   {"audit_reader"},
	"tack_audit_redactor": {"audit_redactor"},
	"tack_audit_operator": {"audit_operator"},
	"tack_app":            {"app_auth"},
	"tack_migrator":       {},
}

func requireRoleSteps(t *testing.T, stepResults []audit.Event) {
	t.Helper()
	if len(stepResults) != len(seededRoleBases) {
		t.Fatalf("the seed recorded %d role step results; the test requires %d", len(stepResults), len(seededRoleBases))
	}
	for _, result := range stepResults {
		var evidence roleStepEvidence
		if err := json.Unmarshal(result.Extra, &evidence); err != nil {
			t.Fatalf("decoding role evidence for event %s failed: %v", result.EventID, err)
		}
		wantBases, known := seededRoleBases[evidence.Role]
		if !known || result.Outcome != audit.OutcomeCompleted || evidence.After == nil {
			t.Fatalf("role step %s has outcome %s for role %q; it should complete for a seeded role and record its state after the seed",
				result.EventID, result.Outcome, evidence.Role)
		}
		after := evidence.After
		if !after.Exists || !after.Login || !evidence.PasswordSet || !slices.Equal(after.MemberOf, wantBases) {
			t.Fatalf("role %s has state %+v and PasswordSet %t; the test requires login, a password, and base roles %v",
				evidence.Role, *after, evidence.PasswordSet, wantBases)
		}
		if evidence.Role != "tack_migrator" && evidence.Before.Exists {
			t.Fatalf("role %s already existed; the test requires the first seed to create it", evidence.Role)
		}
	}
}

func eventsOfOperation(events []audit.Event, operationID uuid.UUID) []audit.Event {
	var selected []audit.Event
	for _, event := range events {
		if event.Context.OperationID == operationID || event.Context.ParentOperationID == operationID {
			selected = append(selected, event)
		}
	}
	return selected
}
