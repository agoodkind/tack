//go:build pgoverlay

// Package pgoverlay_test runs the patched PostgreSQL provider through the
// OpenTofu command line against a local PostgreSQL server and the local
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
	// providerVariable is the environment variable with the path of the built
	// terraform-provider-postgresql file.
	providerVariable = "TACK_PG_OVERLAY_PROVIDER"
	// tofuVariable is the environment variable with the path of the OpenTofu
	// executable.
	tofuVariable = "TACK_PG_OVERLAY_TOFU"
	// tofuVersionVariable is the environment variable with the OpenTofu version
	// that the executable must report.
	tofuVersionVariable = "TACK_PG_OVERLAY_TOFU_VERSION"
)

// TestMain removes the engines this binary started after every test has run.
// A failed removal fails the run. A passing run proves the cleanup worked.
func TestMain(m *testing.M) {
	code := m.Run()
	if err := testenv.Release(context.Background()); err != nil {
		slog.Error("pgoverlay.release_failed", slog.String("err", err.Error()))
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

func requireVariable(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s must be set; make test-postgresql-overlay sets it", name)
	}
	return value
}
