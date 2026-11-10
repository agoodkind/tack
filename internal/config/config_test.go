package config

import (
	"os"
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
