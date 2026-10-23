package integration_test

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

// coverageCase defines an operation to run and the audit verb and outcome it
// must record. An optional check validates the matching ledger rows.
type coverageCase struct {
	run     func(t *testing.T, env *coverageEnv) coverageEvidence
	verb    audit.Verb
	outcome audit.Outcome
	check   func(t *testing.T, env *coverageEnv, rows []audit.Row)
}

// coverageEvidence identifies an operation's audit events by organization and
// request or actor. It can override the expected outcome or select an operator
// outbox instead of the ledger.
type coverageEvidence struct {
	orgID     uuid.UUID
	requestID string
	actorID   uuid.UUID
	outcome   audit.Outcome
	outbox    *pgxpool.Pool
}

// TestRuntimeAuditCoverage requires each registered operation to have a case, a
// pending entry, or an exclusion. It runs cases against real test engines and
// checks their audit events. It also verifies that commands excluded for static
// help print help without a working database.
func TestRuntimeAuditCoverage(t *testing.T) {
	env := startCoverageEnv(t)
	operations := registeredOperations(t, env)
	cases := coverageCases()
	excluded := excludedCoverage()
	requireCaseTables(t, env, operations, cases, excluded)
	for _, key := range operations {
		if exclusion, found := excluded[key]; found && exclusion.helpOnly {
			t.Run(key, func(t *testing.T) { requireStaticHelp(t, key) })
		}
		if current, found := cases[key]; found {
			t.Run(key, func(t *testing.T) { env.runCase(t, current) })
		}
	}
}

// runCase executes an operation and requires its expected audit event. It uses
// the operation's outcome override when present and runs any additional row
// check.
func (env *coverageEnv) runCase(t *testing.T, current coverageCase) {
	t.Helper()
	evidence := current.run(t, env)
	outcome := current.outcome
	if evidence.outcome != "" {
		outcome = evidence.outcome
	}
	rows := env.waitForRows(t, evidence, current.verb, outcome)
	if current.check != nil {
		current.check(t, env, rows)
	}
}

// requireCaseTables checks that each registered operation appears in exactly
// one case, pending entry, or exclusion. It rejects entries for unregistered
// operations and requires a case for each failureOnlyCoverage entry. It also
// fails when a pending command starts recording an audit class.
func requireCaseTables(t *testing.T, env *coverageEnv, operations []string, cases map[string]coverageCase, excluded map[string]coverageExclusion) {
	t.Helper()
	registered := map[string]bool{}
	for _, key := range operations {
		registered[key] = true
	}
	var missing []string
	for _, key := range operations {
		_, hasCase := cases[key]
		_, pending := pendingCoverage[key]
		_, isExcluded := excluded[key]
		if !hasCase && !pending && !isExcluded {
			missing = append(missing, key)
		}
		if (hasCase && pending) || (hasCase && isExcluded) || (pending && isExcluded) {
			t.Errorf("Operation %q appears in multiple coverage tables; each operation must appear in exactly one.", key)
		}
	}
	if len(missing) > 0 {
		t.Errorf("Registered operations do not have a case, pending entry, or exclusion:\n%s", strings.Join(missing, "\n"))
	}
	tableKeys := slices.Concat(slices.Collect(maps.Keys(pendingCoverage)), slices.Collect(maps.Keys(failureOnlyCoverage)),
		slices.Collect(maps.Keys(excluded)))
	for _, key := range tableKeys {
		if !registered[key] {
			t.Errorf("Coverage table entry %q does not match any registered operation.", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(cases)) {
		if !registered[key] {
			t.Errorf("Case %q does not match any registered operation.", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(failureOnlyCoverage)) {
		if _, hasCase := cases[key]; !hasCase {
			t.Errorf("Operation %q in failureOnlyCoverage does not have an executable case.", key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(pendingCoverage)) {
		if class := env.commandClasses[key]; class != "" {
			t.Errorf("Pending operation %q now records audit class %s; replace its pending entry with an executable case.", key, class)
		}
	}
}

// coverageCases returns executable audit cases for MCP operations, HTTP routes,
// and ops commands.
func coverageCases() map[string]coverageCase {
	cases := map[string]coverageCase{}
	addMCPCases(cases)
	addHTTPCases(cases)
	addCommandCases(cases)
	return cases
}
