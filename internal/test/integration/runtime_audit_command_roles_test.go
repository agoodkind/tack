package integration_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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

func requireRoleSteps(t *testing.T, pool *pgxpool.Pool, stepResults []audit.Event) {
	t.Helper()
	baseRoles := []string{
		"app_auth",
		"audit_operator",
		"audit_reader",
		"audit_redactor",
		"audit_writer",
	}
	if len(stepResults) != len(seededRoleBases) {
		t.Fatalf(
			"the seed recorded %d role step results; the test requires %d",
			len(stepResults),
			len(seededRoleBases),
		)
	}

	seen := make(map[string]bool, len(seededRoleBases))

	for _, result := range stepResults {
		var evidence roleStepEvidence
		if err := json.Unmarshal(result.Extra, &evidence); err != nil {
			t.Fatalf("decoding role evidence for event %s failed: %v", result.EventID, err)
		}

		wantBases, known := seededRoleBases[evidence.Role]
		if !known || seen[evidence.Role] || result.Outcome != audit.OutcomeAttempted ||
			evidence.After != nil {
			t.Fatalf(
				"role step %s has outcome %s for role %q; it should complete for a seeded role and record its state after the seed",
				result.EventID,
				result.Outcome,
				evidence.Role,
			)
		}

		seen[evidence.Role] = true

		if evidence.Role != "tack_migrator" && evidence.Before.Exists {
			t.Fatalf(
				"role %s already existed; the test requires the first seed to create it",
				evidence.Role,
			)
		}

		var login bool
		var bases []string
		if err := pool.QueryRow(
			t.Context(),
			readTestSQL(t, "runtime_audit_seeded_role_state.sql"),
			evidence.Role,
			baseRoles,
		).Scan(
			&login,
			&bases,
		); err != nil {
			t.Fatalf("read seeded role %s: %v", evidence.Role, err)
		}

		if !login || !slices.Equal(bases, wantBases) {
			t.Fatalf(
				"seeded role %s has login %t and memberships %v, want login and memberships %v",
				evidence.Role,
				login,
				bases,
				wantBases,
			)
		}
	}
}

func eventsOfOperation(events []audit.Event, operationID uuid.UUID) []audit.Event {
	var selected []audit.Event

	for _, event := range events {
		if event.Context.OperationID == operationID ||
			event.Context.ParentOperationID == operationID {
			selected = append(selected, event)
		}
	}

	return selected
}
