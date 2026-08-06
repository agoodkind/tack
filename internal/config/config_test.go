package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadUses15SecondAuditKafkaProduceTimeoutWhenUnset(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://test")

	previous, wasSet := os.LookupEnv("AUDIT_KAFKA_PRODUCE_TIMEOUT")
	if err := os.Unsetenv("AUDIT_KAFKA_PRODUCE_TIMEOUT"); err != nil {
		t.Fatalf("unset AUDIT_KAFKA_PRODUCE_TIMEOUT: %v", err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("AUDIT_KAFKA_PRODUCE_TIMEOUT", previous)
			return
		}
		_ = os.Unsetenv("AUDIT_KAFKA_PRODUCE_TIMEOUT")
	})

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AuditKafkaProduceTimeout != 15*time.Second {
		t.Fatalf("AuditKafkaProduceTimeout = %s, want 15s", cfg.AuditKafkaProduceTimeout)
	}
}
