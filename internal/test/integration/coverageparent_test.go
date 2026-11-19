//go:build runtimeaudit

package integration_test

import (
	"testing"

	"goodkind.io/tack/internal/audit"
)

func addParentRelationshipBackfillCase(cases map[string]coverageCase) {
	cases["command ops backfill once-parent-relationships"] = commandCase(
		audit.VerbOpsBackfillParentRelationships,
		audit.OutcomeAttempted,
		argsCommand(func(_ *testing.T, _ *coverageEnv) []string {
			return []string{"ops", "backfill", "once-parent-relationships"}
		}),
	)
}
