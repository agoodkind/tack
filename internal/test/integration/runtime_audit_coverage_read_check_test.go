//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
)

func finalReadCheck(verb audit.Verb, outcome audit.Outcome) func(*testing.T, *coverageEnv, []audit.Row) {
	return func(t *testing.T, _ *coverageEnv, rows []audit.Row) {
		t.Helper()
		for _, row := range rows {
			if row.Action == string(verb) && row.Outcome == outcome &&
				row.Context.OperationClass == audit.OperationRead && row.Context.Phase == audit.PhaseRead {
				return
			}
		}
		t.Fatalf("The ledger does not contain a %s read event of class read and phase read for the operator; the events are: %s",
			outcome, describeRows(rows))
	}
}

func readCommandCase(verb audit.Verb, outcome audit.Outcome, input func(*testing.T, *coverageEnv) commandInput) coverageCase {
	current := commandCase(verb, outcome, input)
	current.check = finalReadCheck(verb, outcome)
	return current
}

func failingReadCommandCase(verb audit.Verb, outcome audit.Outcome, input func(*testing.T, *coverageEnv) commandInput) coverageCase {
	current := failingCommandCase(verb, outcome, input)
	current.check = finalReadCheck(verb, outcome)
	return current
}
