//go:build runtimeaudit

package integration_test

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// commandInput supplies command arguments and optional overrides for the server
// configuration and audit database pool.
type commandInput struct {
	args      []string
	configure func(*config.Config)
	outbox    *pgxpool.Pool
}

// commandCase creates an executable command case with an expected audit verb
// and outcome. It permits command errors only when the expected outcome is
// error, failed, or refused.
func commandCase(verb audit.Verb, outcome audit.Outcome, input func(t *testing.T, env *coverageEnv) commandInput) coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			current := input(t, env)
			operatorID, output, err := env.commandWith(t, current)
			expectsFailure := outcome == audit.OutcomeError || outcome == audit.OutcomeFailed || outcome == audit.OutcomeRefused
			if err != nil && !expectsFailure {
				t.Fatalf("The command returned an error when the case required success; the arguments, error, and output follow: %v: %v\n%s", current.args, err, output)
			}
			evidence := operatorEvidence(operatorID)
			evidence.outbox = current.outbox
			return evidence
		},
		verb: verb, outcome: outcome, check: nil,
	}
}

// failingCommandCase creates a case that requires the command to return an
// error and record the expected audit verb and outcome.
func failingCommandCase(verb audit.Verb, outcome audit.Outcome, input func(t *testing.T, env *coverageEnv) commandInput) coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			current := input(t, env)
			operatorID, output, err := env.commandWith(t, current)
			if err == nil {
				t.Fatalf("The command succeeded when the case required an error; the arguments and output follow: %v:\n%s", current.args, output)
			}
			t.Logf("The command returned the expected error; the arguments and error follow: %v: %v", current.args, err)
			evidence := operatorEvidence(operatorID)
			evidence.outbox = current.outbox
			return evidence
		},
		verb: verb, outcome: outcome, check: nil,
	}
}

// searchCommandCase runs withoutSearch when the environment lacks OpenSearch
// and withSearch when it includes OpenSearch. It uses the second case's audit
// expectations unless the selected run overrides the outcome.
func searchCommandCase(withoutSearch, withSearch coverageCase) coverageCase {
	return coverageCase{
		run: func(t *testing.T, env *coverageEnv) coverageEvidence {
			if env.searchEnabled {
				return withSearch.run(t, env)
			}
			return withoutSearch.run(t, env)
		},
		verb: withSearch.verb, outcome: withSearch.outcome, check: nil,
	}
}

// fixedCommand returns an input function that uses the supplied command
// arguments without configuration or database pool overrides.
func fixedCommand(args ...string) func(*testing.T, *coverageEnv) commandInput {
	return func(*testing.T, *coverageEnv) commandInput {
		return commandInput{args: args, configure: nil, outbox: nil}
	}
}

// argsCommand returns an input function that derives command arguments from the
// test environment without configuration or database pool overrides.
func argsCommand(args func(t *testing.T, env *coverageEnv) []string) func(*testing.T, *coverageEnv) commandInput {
	return func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: args(t, env), configure: nil, outbox: nil}
	}
}

func addCommandCases(cases map[string]coverageCase) {
	addNodeCommandCases(cases)
	addAuditCommandCases(cases)
	addAuthCommandCases(cases)
	addBackfillCommandCases(cases)
	addDatagenCommandCases(cases)
	addInfrastructureCommandCases(cases)
	addSearchControlCases(cases)
	addBackupCommandCases(cases)
	addLedgerCommandCases(cases)
	addStoreCommandCases(cases)
	addRepairBackfillCases(cases)
}
