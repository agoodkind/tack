package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

func writeSecret(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "service-secret")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadResolvesServiceSecretFilesWithoutExportingValues(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", writeSecret(t, "postgres://fixture\n"))
	t.Setenv("OPENSEARCH_PASSWORD", "")
	t.Setenv("OPENSEARCH_PASSWORD_FILE", writeSecret(t, " fixture-password \n"))
	t.Setenv("AUDIT_WRITER_DSN", "")
	t.Setenv("AUDIT_WRITER_DSN_FILE", writeSecret(t, "postgres://writer-fixture"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != "postgres://fixture" || cfg.SearchPassword != " fixture-password " || cfg.AuditWriterDSN != "postgres://writer-fixture" {
		t.Fatal("configuration did not resolve the referenced values")
	}
	for _, name := range []string{"DATABASE_URL", "OPENSEARCH_PASSWORD", "AUDIT_WRITER_DSN"} {
		if os.Getenv(name) != "" {
			t.Fatalf("loading configuration exported %s", name)
		}
	}
}

func TestLoadRejectsInvalidServiceSecretReferences(t *testing.T) {
	for _, scenario := range []string{"conflicting", "missing", "directory", "empty", "blank", "unset-path"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("DATABASE_URL", "postgres://local-fixture")
			t.Setenv("OPENSEARCH_PASSWORD", "")
			path := writeSecret(t, "fixture-must-not-appear-in-errors")
			switch scenario {
			case "conflicting":
				t.Setenv("OPENSEARCH_PASSWORD", "fixture-must-not-appear-in-errors")
			case "missing":
				path = filepath.Join(t.TempDir(), "missing")
			case "directory":
				path = t.TempDir()
			case "empty":
				path = writeSecret(t, "")
			case "blank":
				path = writeSecret(t, " \r\n")
			case "unset-path":
				path = ""
			}
			t.Setenv("OPENSEARCH_PASSWORD_FILE", path)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), "OPENSEARCH_PASSWORD") {
				t.Fatal("the invalid secret reference did not identify its setting")
			}
			if strings.Contains(err.Error(), "fixture-must-not-appear-in-errors") {
				t.Fatal("the configuration error disclosed secret content")
			}
		})
	}
}

func TestLoadIgnoresMaintenanceSecret(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://local-fixture")
	t.Setenv("OPENSEARCH_MAINTENANCE_PASSWORD_FILE", filepath.Join(t.TempDir(), "unreadable"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("application configuration tried to read a maintenance secret")
	}
	if cfg.DatabaseURL != "postgres://local-fixture" {
		t.Fatal("application configuration changed its own database")
	}
	if _, err := config.LoadSearchMaintenanceCredentials(); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("maintenance configuration accepted an unreadable secret")
	}
}
