//go:build runtimeaudit

package integration_test

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// topLevelCommand creates a case that runs the supplied arguments and requires
// a successful exit. It pairs the runner with the expected audit verb and
// outcome.
func topLevelCommand(verb audit.Verb, outcome audit.Outcome, args func(t *testing.T, env *topLevelEnv) []string) topLevelCase {
	return topLevelCase{
		run: func(t *testing.T, env *topLevelEnv) uuid.UUID {
			current := args(t, env)
			operatorID, output, err := env.run(t, current...)
			if err != nil {
				t.Fatalf("The command failed; the test requires a successful exit for arguments %v: %v\n%s", current, err, output)
			}
			return operatorID
		},
		verb: verb, outcome: outcome,
	}
}

// window returns audit query arguments for the test organization and a time
// range from one hour before to one hour after the current time.
func (env *topLevelEnv) window() []string {
	now := time.Now().UTC()
	return []string{
		"--org", env.orgID.String(), "--oldest", now.Add(-time.Hour).Format(time.RFC3339),
		"--latest", now.Add(time.Hour).Format(time.RFC3339),
	}
}

// exportArgs adds the test organization's time range and a temporary output
// directory to an export command.
func exportArgs(command ...string) func(*testing.T, *topLevelEnv) []string {
	return func(t *testing.T, env *topLevelEnv) []string {
		return slices.Concat(command, env.window(), []string{"--out", filepath.Join(t.TempDir(), "bundle")})
	}
}

// verifyArgs exports an audit bundle to a temporary directory and returns
// arguments for verifying that bundle.
func verifyArgs(command ...string) func(*testing.T, *topLevelEnv) []string {
	return func(t *testing.T, env *topLevelEnv) []string {
		bundle := filepath.Join(t.TempDir(), "bundle")
		if _, output, err := env.run(t, slices.Concat([]string{"audit", "export"}, env.window(), []string{"--out", bundle})...); err != nil {
			t.Fatalf("Audit export failed; the verification case requires an exported bundle: %v\n%s", err, output)
		}
		return slices.Concat(command, []string{"--bundle", bundle})
	}
}

// keyArgs adds a temporary PEM output path to a key generation command.
func keyArgs(command ...string) func(*testing.T, *topLevelEnv) []string {
	return func(t *testing.T, _ *topLevelEnv) []string {
		return slices.Concat(command, []string{filepath.Join(t.TempDir(), "generated.pem")})
	}
}

// fixedArgs returns an argument function that uses the supplied command
// unchanged.
func fixedArgs(command ...string) func(*testing.T, *topLevelEnv) []string {
	return func(*testing.T, *topLevelEnv) []string { return command }
}

// topLevelCases returns executable cases for server startup, migration,
// seeding, audit commands, and hidden aliases with their expected audit verbs
// and outcomes.
func topLevelCases() map[string]topLevelCase {
	serve := func(args ...string) topLevelCase {
		return topLevelCase{
			run:  func(t *testing.T, env *topLevelEnv) uuid.UUID { return env.runServe(t, args...) },
			verb: audit.VerbServerServe, outcome: audit.OutcomeOK,
		}
	}
	return map[string]topLevelCase{
		"command tack":                    serve(),
		"command tack serve":              serve("serve"),
		"command tack migrate":            topLevelCommand(audit.VerbDatabaseMigrate, audit.OutcomeCompleted, fixedArgs("migrate")),
		"command tack seed":               topLevelCommand(audit.VerbBootstrapSeed, audit.OutcomeOK, fixedArgs("seed", "--allow-reseed")),
		"command tack audit export":       topLevelCommand(audit.VerbAuditExport, audit.OutcomeOK, exportArgs("audit", "export")),
		"command tack audit-export":       topLevelCommand(audit.VerbAuditExport, audit.OutcomeOK, exportArgs("audit-export")),
		"command tack audit verify":       topLevelCommand(audit.VerbAuditVerify, audit.OutcomeOK, verifyArgs("audit", "verify")),
		"command tack audit-verify":       topLevelCommand(audit.VerbAuditVerify, audit.OutcomeOK, verifyArgs("audit-verify")),
		"command tack audit gen-key":      topLevelCommand(audit.VerbAuditKeyGenerate, audit.OutcomeOK, keyArgs("audit", "gen-key")),
		"command tack gen-audit-key":      topLevelCommand(audit.VerbAuditKeyGenerate, audit.OutcomeOK, keyArgs("gen-audit-key")),
		"command tack audit signers":      topLevelCommand(audit.VerbAuditSignersVerified, audit.OutcomeOK, signersArgs),
		"command tack audit query":        topLevelCommand(audit.VerbAuditRead, audit.OutcomeOK, queryArgs),
		"command tack audit get":          topLevelCommand(audit.VerbAuditRead, audit.OutcomeOK, getArgs),
		"command tack audit redact-actor": topLevelCommand(audit.VerbAuditPIIRedacted, audit.OutcomeOK, redactArgs),
	}
}

func signersArgs(*testing.T, *topLevelEnv) []string {
	return []string{"audit", "signers", "--since", time.Now().UTC().Format(time.RFC3339)}
}

func queryArgs(_ *testing.T, env *topLevelEnv) []string {
	return slices.Concat([]string{"audit", "query"}, env.window())
}

func getArgs(_ *testing.T, env *topLevelEnv) []string {
	return []string{"audit", "get", "--event-id", env.eventID.String()}
}

func redactArgs(_ *testing.T, env *topLevelEnv) []string {
	return []string{"audit", "redact-actor", "--org", env.orgID.String(), "--actor-id", env.actorID.String()}
}
