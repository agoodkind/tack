// Package testenv gives tests a real YugabyteDB ledger, PostgreSQL server,
// FoundationDB cluster, OpenSearch engine, and SeaweedFS object store.
// Each process starts its engines as containers with the Docker SDK.
// [Release] removes the engines at the end of TestMain. The signal handler
// removes them on SIGINT or SIGTERM. A watcher process removes them when a
// test binary ends in any other way. A later nonisolated test process removes
// the engines of a process when its watcher could not run. The ledger and
// FoundationDB images match the live stores in docker-compose.yml. The object
// store uses the release pinned by the configs repository.
//
// A test that needs a store calls [Ledger], [PostgreSQL], [FoundationDB],
// [OpenSearch], or [ObjectStore]. When the Docker daemon does not answer,
// the test fails with the reason. A `go test -short` run skips it.
// OpenSearch tests require [SearchIntegrationVariable] to be "1".
// cmd/testenv runs the same helpers
// for an operator. Its `down` subcommand removes operator engines and the
// engines of test processes that have exited, and keeps the engines of test
// processes that still run.
package testenv

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"
)

// T defines the subset of [testing.TB] that the helpers use. [testing.T] and
// cmd/testenv implement it. Failures write a reason to Output and call FailNow
// instead of using a variadic formatting method with empty interface arguments.
type T interface {
	Helper()
	Context() context.Context
	Output() io.Writer
	FailNow()
	SkipNow()
}

// provisionTimeout bounds one engine's start, readiness, and preparation.
const provisionTimeout = 20 * time.Minute

// provisioned stores one provisioning result or error for the process.
// Every caller receives the first caller's result.
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

// Ledger returns the connection string for this process's fully migrated
// YugabyteDB ledger. Tests connect as the engine superuser and can create
// roles and databases.
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
