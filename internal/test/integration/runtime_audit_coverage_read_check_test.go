//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
)

func withReadCheck(base coverageCase, verb audit.Verb, outcome audit.Outcome) coverageCase {
	base.check = func(t *testing.T, _ *coverageEnv, rows []audit.Row) {
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
	return base
}
