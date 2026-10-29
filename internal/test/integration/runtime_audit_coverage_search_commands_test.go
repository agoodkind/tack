//go:build runtimeaudit

package integration_test

import (
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/searchaccess"
)

// addSearchControlCases adds the provision, settings, access rollout, and
// access rollback cases. Provision refuses to run without OpenSearch
// settings. The settings command fails without OpenSearch settings and
// reads the point-in-time limit when the environment includes OpenSearch.
// The rollout commands change the FoundationDB rollout record of a new
// authority.
func addSearchControlCases(cases map[string]coverageCase) {
	cases["command ops search settings"] = searchCommandCase(
		failingCommandCase(audit.VerbOpsSearchSettings, audit.OutcomeOK, fixedCommand("ops", "search", "settings")),
		commandCase(audit.VerbOpsSearchSettings, audit.OutcomeOK, fixedCommand("ops", "search", "settings")),
	)
	cases["command ops search provision"] = failingCommandCase(audit.VerbOpsSearchProvision, audit.OutcomeRefused,
		fixedCommand("ops", "search", "provision"))
	cases["command ops search access-rollout"] = commandCase(audit.VerbOpsSearchAccessRollout, audit.OutcomeCompleted,
		func(*testing.T, *coverageEnv) commandInput {
			return commandInput{args: accessRolloutArgs(uuid.NewString()), configure: nil, outbox: nil}
		})
	cases["command ops search access-rollback"] = commandCase(audit.VerbOpsSearchAccessRollback, audit.OutcomeOK,
		func(t *testing.T, env *coverageEnv) commandInput {
			authority := uuid.NewString()
			if _, output, err := env.command(t, nil, accessRolloutArgs(authority)...); err != nil {
				t.Fatalf("Starting an access rollout failed; the rollback case requires a rollout in progress: %v\n%s", err, output)
			}
			return commandInput{args: []string{"ops", "search", "access-rollback", "--authority", authority}, configure: nil, outbox: nil}
		})
}

func accessRolloutArgs(authority string) []string {
	return []string{"ops", "search", "access-rollout", "--authority", authority, "--candidate", searchaccess.RotatedVersion}
}
