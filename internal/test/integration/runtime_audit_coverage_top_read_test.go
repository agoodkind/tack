//go:build runtimeaudit

package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

func (env *topLevelEnv) requireFinalReadEvent(t *testing.T, operatorID uuid.UUID, verb audit.Verb, outcome audit.Outcome) {
	t.Helper()
	for _, event := range readOutboxEvents(t, env.admin, verb, operatorID) {
		if event.Outcome == outcome && event.Context.OperationClass == audit.OperationRead && event.Context.Phase == audit.PhaseRead {
			return
		}
	}
	t.Fatalf("The operator outbox did not contain a final read event; the operator, verb, and outcome follow: %s %s %s", operatorID, verb, outcome)
}
