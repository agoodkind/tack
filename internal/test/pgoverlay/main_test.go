//go:build pgoverlay

// Package pgoverlay_test runs the built patched PostgreSQL provider through
// OpenTofu in a container, against a local PostgreSQL server and the local
// YugabyteDB engine that internal/testenv starts.
package pgoverlay_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

const (
	// tofuVersionVariable is the environment variable with the OpenTofu version
	// that the runner image installs.
	tofuVersionVariable = "TACK_PG_OVERLAY_TOFU_VERSION"
	// networkVariable is the environment variable with the name of the Compose
	// project network. The engines of this run join that network, and Compose
	// removes it with the project.
	networkVariable = "TACK_PG_OVERLAY_NETWORK"
	// projectVariable is the environment variable with the Compose project
	// name. Compose removes the engines of this run with that project.
	projectVariable = "TACK_PG_OVERLAY_PROJECT"
)

// TestMain isolates this run from other test runs, creates a decoy that looks
// like the leftover of a stopped run, runs the tests, and checks that the decoy
// still exists. A decoy that disappeared means this run swept another run's
// engines. TestMain then removes the decoy and the engines of this run. A failed
// check or removal fails the run.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()
	if err := testenv.IsolateRun(os.Getenv(networkVariable), os.Getenv(projectVariable)); err != nil {
		slog.Error("pgoverlay.isolate_failed", slog.String("err", err.Error()))
		return 1
	}
	if err := testenv.IsolateRun(os.Getenv(networkVariable)+"-replacement", os.Getenv(projectVariable)+"-replacement"); err == nil {
		slog.Error("pgoverlay.repeated_isolation_accepted", slog.String("err", "second configuration did not return an error"))
		return 1
	}
	decoy, err := testenv.CreateStoppedRunDecoy(ctx)
	if err != nil {
		slog.Error("pgoverlay.decoy_failed", slog.String("err", err.Error()))
		return 1
	}
	code := 0
	if err := checkDecoyProject(ctx, decoy, os.Getenv(projectVariable)); err != nil {
		slog.Error("pgoverlay.project_changed", slog.String("err", err.Error()))
		code = 1
	} else {
		code = m.Run()
	}
	failures := []error{checkDecoy(ctx, decoy), testenv.RemoveStoppedRunDecoy(ctx, decoy), testenv.Release(ctx)}
	for _, err := range failures {
		if err != nil {
			slog.Error("pgoverlay.cleanup_failed", slog.String("err", err.Error()))
			code = 1
		}
	}
	return code
}

// checkDecoy returns an error when the decoy no longer exists.
func checkDecoy(ctx context.Context, decoy string) error {
	exists, err := testenv.StoppedRunDecoyExists(ctx, decoy)
	if err != nil {
		return err
	}
	if !exists {
		return &decoyRemovedError{name: decoy}
	}
	return nil
}

type decoyRemovedError struct {
	name string
}

func (err *decoyRemovedError) Error() string {
	return "the stopped-run decoy " + err.name + " was removed during this run"
}

func requireVariable(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s must be set; make test-postgresql-overlay sets it", name)
	}
	return value
}
