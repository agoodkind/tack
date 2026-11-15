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
	datagenSweepIdle        = "30s"
	datagenSweepAbsolute    = "2m"
)

// TestSearchSweepProofDatagenFailsWhenSweepFails requires the public command
// to report a failed cleanup after stored session headers become invalid.
func TestSearchSweepProofDatagenFailsWhenSweepFails(t *testing.T) {
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
					storedKey := fdb.Key(
						append(bytes.Clone(fdbadapter.TestPrefixRange()), packed...),
					)
					integration.ReplaceRawValue(t, storedKey, []byte(invalidHeaderValue))
				}
			},
		},
	)
	_, err := runDatagenSearchCommand(t, cfg, "--commit")
	hooks.requireFinished(t)
	if err == nil || !strings.Contains(err.Error(), datagenSweepFailed) {
		t.Fatalf(
			"ops qa datagen search after a failed sweep returned %v, want text %q",
			err,
			datagenSweepFailed,
		)
	}
}
