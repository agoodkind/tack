package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

// unsetForTest removes one variable for the test and restores it after.
func unsetForTest(t *testing.T, name string) {
	t.Helper()
	previous, wasSet := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatalf("unset %s: %v", name, err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(name, previous)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func setRequiredForTest(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("MEILI_URL", "http://meilisearch:7700")
	t.Setenv("MEILI_MASTER_KEY", "test-key")
}

func TestLoadUses15SecondAuditKafkaProduceTimeoutWhenUnset(t *testing.T) {
	setRequiredForTest(t)
	unsetForTest(t, "AUDIT_KAFKA_PRODUCE_TIMEOUT")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuditKafkaProduceTimeout != 15*time.Second {
		t.Fatalf("AuditKafkaProduceTimeout = %s, want 15s", cfg.AuditKafkaProduceTimeout)
	}
}

// A missing or empty search address or key refuses the load and names the
// variable, instead of starting against a compiled-in default or an empty
// rendered value (TACK-268).
func TestLoadRefusesMissingSearchValues(t *testing.T) {
	for _, name := range []string{"MEILI_URL", "MEILI_MASTER_KEY"} {
		t.Run(name+" unset", func(t *testing.T) {
			setRequiredForTest(t)
			unsetForTest(t, name)
			assertLoadRefusesNaming(t, name)
		})
		t.Run(name+" empty", func(t *testing.T) {
			setRequiredForTest(t)
			t.Setenv(name, "")
			assertLoadRefusesNaming(t, name)
		})
	}
}

func assertLoadRefusesNaming(t *testing.T, name string) {
	t.Helper()
	cfg, err := Load()
	if err == nil {
		t.Fatalf("Load returned no error; cfg.MeiliURL=%q", cfg.MeiliURL)
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("Load error %q does not name %s", err.Error(), name)
	}
}
