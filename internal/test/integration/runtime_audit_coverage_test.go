//go:build runtimeaudit

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

type coverageCase struct {
	run     func(t *testing.T, env *coverageEnv) coverageEvidence
	verb    audit.Verb
	outcome audit.Outcome
	check   func(t *testing.T, env *coverageEnv, rows []audit.Row)
}

type coverageEvidence struct {
	orgID     uuid.UUID
	requestID string
	actorID   uuid.UUID
	outcome   audit.Outcome
	outbox    *pgxpool.Pool
	// body is the exact response payload of a read case. A completed read
	// event must record its SHA-256 digest. A nil body skips that comparison.
	body []byte
	// targetID is the node that the case acted on. One format 2 event of the
	// case must target it or list it in a read manifest. uuid.Nil skips that
	// comparison.
	targetID uuid.UUID
}

// TestRuntimeAuditCoverage requires each registered operation to have a case, a
// pending entry, or an exclusion. An operation can have more than one case,
// keyed by caseVariantSeparator. It runs cases against real test engines and
// checks their audit events. It also verifies that commands excluded for static
// help print help without a working database.
func TestRuntimeAuditCoverage(t *testing.T) {
	env := startCoverageEnv(t)
	operations := registeredOperations(t, env)
	cases := coverageCases()
	excluded := excludedCoverage()
	requireCaseTables(t, env, operations, cases, excluded)
	byOperation := operationCases(cases)
	for _, key := range operations {
		if exclusion, found := excluded[key]; found && exclusion.helpOnly {
			t.Run(key, func(t *testing.T) { requireStaticHelp(t, key) })
		}
		for _, caseKey := range byOperation[key] {
			current := cases[caseKey]
			t.Run(caseKey, func(t *testing.T) { env.runCase(t, current) })
		}
	}
}

// caseVariantSeparator separates an operation key from the name of another
// case of the same operation, as in
// "command ops backup restore-drill#fdb-leg-skipped".
const caseVariantSeparator = "#"

// caseOperation returns the registered operation that the case key covers.
func caseOperation(key string) string {
	operation, _, _ := strings.Cut(key, caseVariantSeparator)
	return operation
}

// operationCases returns the sorted case keys of each operation.
func operationCases(cases map[string]coverageCase) map[string][]string {
	byOperation := map[string][]string{}
	for _, key := range slices.Sorted(maps.Keys(cases)) {
		operation := caseOperation(key)
		byOperation[operation] = append(byOperation[operation], key)
	}
	return byOperation
}

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

func requireCaseTables(t *testing.T, env *coverageEnv, operations []string, cases map[string]coverageCase, excluded map[string]coverageExclusion) {
	t.Helper()
	registered := map[string]bool{}
	for _, key := range operations {
		registered[key] = true
	}
	byOperation := operationCases(cases)
	var missing []string
	for _, key := range operations {
		hasCase := len(byOperation[key]) > 0
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
		if !registered[caseOperation(key)] {
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

func coverageCases() map[string]coverageCase {
	cases := map[string]coverageCase{}
	addMCPCases(cases)
	addHTTPCases(cases)
	addCommandCases(cases)
	return cases
}
