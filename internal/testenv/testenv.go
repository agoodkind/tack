// Package testenv gives tests a real YugabyteDB ledger and a real
// FoundationDB cluster. It starts each engine once as a container through the
// Docker SDK, reuses it across test binaries and runs, and gives every test
// binary its own migrated ledger database, so packages that share the engine
// can run in parallel. The engine images are the ones docker-compose.yml runs
// for the live stores.
//
// A test that needs a store calls [Ledger] or [FoundationDB]. When the Docker
// daemon cannot be reached the test fails with the reason; only a
// `go test -short` run skips it.
package testenv

import (
	"context"
	"sync"
	"testing"
	"time"
)

// provisionTimeout bounds one process's whole provisioning step, including
// the wait for other test binaries that hold the ledger lock; readyTimeout
// bounds an engine's own start. The ledger image runs under emulation on an
// arm64 host (see ledgerPlatform), where a cold start and a migration each
// take minutes.
const (
	provisionTimeout = 25 * time.Minute
	readyTimeout     = 10 * time.Minute
)

// provisioned holds the outcome of one process-wide provisioning step, so
// every test in the binary shares the first caller's result or its error.
type provisioned struct {
	once   sync.Once
	result string
	err    error
}

// get runs provision once for the process and returns its result.
func (state *provisioned) get(tb testing.TB, provision func(context.Context) (string, error)) string {
	tb.Helper()
	state.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(tb.Context()), provisionTimeout)
		defer cancel()
		state.result, state.err = provision(ctx)
	})
	if state.err != nil {
		tb.Fatalf("testenv: %v", state.err)
	}
	return state.result
}

var (
	ledgerState       provisioned
	foundationDBState provisioned
	dockerState       provisioned
)

// skipWhenShort skips a store-backed test under `go test -short`, the one
// case where a test may run without its store.
func skipWhenShort(tb testing.TB) {
	tb.Helper()
	if testing.Short() {
		tb.Skip("testenv: store-backed test skipped under -short")
	}
}

// Ledger returns the connection string of a YugabyteDB database that belongs
// to this test binary and carries every migration. The login is the engine
// superuser, so a test may create roles and databases.
func Ledger(tb testing.TB) string {
	tb.Helper()
	skipWhenShort(tb)
	return ledgerState.get(tb, provisionLedger)
}

// FoundationDB returns the path of a cluster file for a configured
// single-node FoundationDB cluster.
func FoundationDB(tb testing.TB) string {
	tb.Helper()
	skipWhenShort(tb)
	return foundationDBState.get(tb, provisionFoundationDB)
}

// RequireDocker fails the test unless the local Docker daemon answers, for
// tests that drive the daemon themselves.
func RequireDocker(tb testing.TB) {
	tb.Helper()
	skipWhenShort(tb)
	dockerState.get(tb, pingDocker)
}
