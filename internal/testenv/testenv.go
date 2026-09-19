// Package testenv gives tests a real YugabyteDB ledger and a real
// FoundationDB cluster. Each process starts its own engines as containers
// through the Docker SDK, so test binaries running in parallel share no
// engine state, and removes them when [Release] runs at the end of the
// binary's TestMain. The engine images are the ones docker-compose.yml runs
// for the live stores.
//
// A test that needs a store calls [Ledger] or [FoundationDB]. When the Docker
// daemon cannot be reached the test fails with the reason; only a
// `go test -short` run skips it. cmd/testenv drives the same helpers for an
// operator, and its `down` subcommand removes every engine by label.
package testenv

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// T is the part of [testing.TB] the helpers use. A [testing.T] satisfies it,
// and cmd/testenv supplies its own. It carries no Fatalf because a variadic
// formatting method would put an empty interface in this package's
// signatures; a failure writes its reason to Output and calls FailNow.
type T interface {
	Helper()
	Context() context.Context
	Output() io.Writer
	FailNow()
	SkipNow()
}

// provisionTimeout bounds one engine's start, readiness, and preparation.
// The ledger image runs under emulation on an arm64 host (see
// ledgerPlatform), where a cold start and a migration each take minutes.
const provisionTimeout = 20 * time.Minute

// provisioned holds the outcome of one process-wide provisioning step, so
// every caller in the process shares the first caller's result or its error.
type provisioned struct {
	once   sync.Once
	result string
	err    error
}

// get runs provision once for the process and returns its result.
func (state *provisioned) get(t T, provision func(context.Context) (string, error)) string {
	t.Helper()
	state.once.Do(func() {
		ctx, cancel := context.WithTimeout(t.Context(), provisionTimeout)
		defer cancel()
		state.result, state.err = provision(ctx)
	})
	if state.err != nil {
		_, _ = fmt.Fprintf(t.Output(), "testenv: %v\n", state.err)
		t.FailNow()
	}
	return state.result
}

var (
	ledgerState       provisioned
	foundationDBState provisioned
	dockerState       provisioned
)

// skipWhenShort skips a store-backed test under `go test -short`, the one
// case where a test may run without its store. Outside a test binary there
// is no -short flag to read.
func skipWhenShort(t T) {
	t.Helper()
	if testing.Testing() && testing.Short() {
		_, _ = fmt.Fprintln(t.Output(), "testenv: store-backed test skipped under -short")
		t.SkipNow()
	}
}

// Ledger returns the connection string of this process's YugabyteDB ledger,
// which carries every migration. The login is the engine superuser, so a
// test may create roles and databases.
func Ledger(t T) string {
	t.Helper()
	skipWhenShort(t)
	return ledgerState.get(t, provisionLedger)
}

// FoundationDB returns the path of a cluster file for this process's
// configured single-node FoundationDB cluster.
func FoundationDB(t T) string {
	t.Helper()
	skipWhenShort(t)
	return foundationDBState.get(t, provisionFoundationDB)
}

// RequireDocker fails the test unless the local Docker daemon answers, for
// tests that drive the daemon themselves.
func RequireDocker(t T) {
	t.Helper()
	skipWhenShort(t)
	dockerState.get(t, pingDocker)
}
