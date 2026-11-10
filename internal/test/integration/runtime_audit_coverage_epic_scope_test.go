//go:build runtimeaudit

package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// epicScopeCaseKey is the map key for the tack_create_issue runtime audit case
// that supplies epic_reference without project_reference.
const epicScopeCaseKey = "mcp tool tack_create_issue" + caseVariantSeparator + "epic-reference-only"

// addEpicScopeCase adds a runtime audit case for tack_create_issue with
// epic_reference and without project_reference. The case checks the ledger scope.
func addEpicScopeCase(cases map[string]coverageCase) {
	var issueID uuid.UUID
	cases[epicScopeCaseKey] = coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			fixture := env.fixture(t)
			arguments := map[string]any{
				"workspace_reference": env.server.workspace,
				"epic_reference":      fixture.nodes["epic"].String(),
				"name":                "Coverage epic-scoped issue " + uuid.NewString()[:8],
			}
			evidence, text := env.toolCall(t, "tack_create_issue", arguments)
			issueID = uuid.MustParse(rawID(t, text))
			evidence.targetID = issueID
			return evidence
		},
		verb: audit.VerbNodeCreate, outcome: audit.OutcomeCompleted,
		check: func(t *testing.T, env *coverageEnv, rows []audit.Row) {
			t.Helper()
			fixture := env.fixture(t)
			requireCreateScope(t, rows, issueID, fixture.nodes["project"], fixture.nodes["epic"])
		},
	}
}

// requireCreateScope fails unless a completed node creation row targets issueID
// and every matching row records projectID as its scope.
func requireCreateScope(t *testing.T, rows []audit.Row, issueID, projectID, epicID uuid.UUID) {
	t.Helper()
	checked := false
	for _, row := range rows {
		target := row.Context.Target
		if row.Action != string(audit.VerbNodeCreate) || row.Outcome != audit.OutcomeCompleted || target == nil || target.ID != issueID {
			continue
		}
		checked = true
		if row.Context.ScopeID == epicID {
			t.Fatalf("Event %s recorded the epic %s as the scope of issue %s; the project %s is the scope.", row.EventID, epicID, issueID, projectID)
		}
		if row.Context.ScopeID != projectID {
			t.Fatalf("Event %s recorded scope %s for issue %s, want the project %s; the event context follows: %+v",
				row.EventID, row.Context.ScopeID, issueID, projectID, row.Context)
		}
	}
	if !checked {
		t.Fatalf("No completed node create event targeted issue %s; the rows follow: %s", issueID, describeRows(rows))
	}
}
