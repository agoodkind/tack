//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/apple/foundationdb/bindings/go/src/fdb"
	"github.com/apple/foundationdb/bindings/go/src/fdb/tuple"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	datagenSessionAbandoned = "qa.datagen.session_abandoned"
	datagenSweepFailed      = "sweep the abandoned session"
	datagenDeleteMissing    = "did not store a completed delete of the header of search session"
	datagenSweepIdle        = "30s"
	datagenSweepAbsolute    = "2m"
)

// TestSearchSweepProofDatagenFailsWhenSweepFails runs ops qa datagen
// search through Cobra with real engines. After the run abandons its
// session, the test replaces every stored session header with invalid
// JSON, causing every later sweep of those sessions to fail. The test
// checks that the command returns the abandoned session sweep error.
func TestSearchSweepProofDatagenFailsWhenSweepFails(t *testing.T) {
	err := runDatagenSearchWithDamagedSessions(
		t,
		func(key fdb.Key) {
			integration.ReplaceRawValue(t, key, []byte(invalidHeaderValue))
		},
	)
	if err == nil || !strings.Contains(err.Error(), datagenSweepFailed) {
		t.Fatalf(
			"ops qa datagen search after a failed sweep returned %v, want text %q",
			err,
			datagenSweepFailed,
		)
	}
}

// TestSearchSweepProofDatagenFailsWithoutHeaderDelete runs ops qa
// datagen search through Cobra with real engines. After the run abandons
// its session, the test clears every stored session header without an
// audit event. The expired-session response and session verb report
// still pass. The test checks that the command returns an error for the
// missing completed header delete.
func TestSearchSweepProofDatagenFailsWithoutHeaderDelete(t *testing.T) {
	err := runDatagenSearchWithDamagedSessions(t, func(key fdb.Key) { clearAttemptRecord(t, key) })
	if err == nil || !strings.Contains(err.Error(), datagenDeleteMissing) {
		t.Fatalf(
			"ops qa datagen search without a header delete returned %v, want text %q",
			err,
			datagenDeleteMissing,
		)
	}
}

// runDatagenSearchWithDamagedSessions runs ops qa datagen search --commit
// with short session deadlines, the relay, the audit consumer, and an
// audit_reader login as AUDIT_READER_DSN. When the run logs the abandoned
// session, a log hook applies damage to the key of every stored session
// header. The function returns the command error.
func runDatagenSearchWithDamagedSessions(t *testing.T, damage func(fdb.Key)) error {
	t.Helper()
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", datagenSweepIdle)
	t.Setenv("OPENSEARCH_SESSION_ABSOLUTE_TIMEOUT", datagenSweepAbsolute)
	cfg := newCohortEngines(t)
	stack := startRuntimeAuditStack(t)
	startPublicLedgerOn(t, &publicServer{clusterFile: cfg.FDBClusterFile}, stack)
	cfg.AuditReaderDSN = roleLoginDSN(t, stack.admin, testenv.Ledger(t), auditReaderRole)
	hooks := installRetireLogHooks(
		t,
		"",
		map[string]func(){
			datagenSessionAbandoned: func() {
				for _, key := range stateKeysOf(
					readStateKeys(t, parentEdgeFixture{cfg: cfg}),
					sessionHeaderFamily,
				) {
					packed := tuple.Tuple(key.elements).Pack()
					damage(fdb.Key(append(bytes.Clone(fdbadapter.TestPrefixRange()), packed...)))
				}
			},
		},
	)
	_, err := runDatagenSearchCommand(t, cfg, "--commit")
	hooks.requireFinished(t)
	return err
}
