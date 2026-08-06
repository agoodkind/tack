package datagen

import (
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

func TestValidateTargetRejectsEmptyAllowTarget(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		DatabaseURL: "postgres://user@yugabyte/tack",
	}
	err := ValidateTarget(cfg)
	if err == nil || !strings.Contains(err.Error(), "TACK_DATAGEN_ALLOW_TARGET") {
		t.Fatalf("ValidateTarget() error = %v, want missing-marker refusal", err)
	}
}

func TestValidateTargetAllowsQA(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		DatabaseURL:        "postgres://user@yugabyte/tack",
		DatagenAllowTarget: " QA ",
	}
	if err := ValidateTarget(cfg); err != nil {
		t.Fatalf("ValidateTarget() error = %v", err)
	}
}

func TestValidateTargetRejectsProductionDespiteAllowTarget(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name      string
		configure func(*config.Config, string)
		value     string
	}{
		{name: "database hostname", configure: setDatabaseURL, value: "postgres://user@tack.home.goodkind.io/tack"},
		{name: "database IPv6", configure: setDatabaseURL, value: "postgres://user@[3d06:bad:b01::117]/tack"},
		{name: "database expanded IPv6", configure: setDatabaseURL, value: "postgres://user@[3d06:0bad:0b01:0:0:0:0:117]/tack"},
		{name: "audit writer", configure: setAuditWriterDSN, value: "postgres://user@tack.home.goodkind.io/tack"},
		{name: "audit reader", configure: setAuditReaderDSN, value: "postgres://user@tack.home.goodkind.io/tack"},
		{name: "audit redactor", configure: setAuditRedactorDSN, value: "postgres://user@tack.home.goodkind.io/tack"},
		{name: "audit ClickHouse", configure: setAuditClickHouseDSN, value: "clickhouse://user@tack.home.goodkind.io/audit"},
		{name: "audit Kafka", configure: setAuditKafkaBrokers, value: "kafka:9092,[3d06:0bad:0b01:0:0:0:0:117]:9092"},
		{name: "Meilisearch", configure: setMeiliURL, value: "https://tack.home.goodkind.io"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := &config.Config{
				DatabaseURL:        "postgres://user@yugabyte/tack",
				DatagenAllowTarget: "qa",
			}
			testCase.configure(cfg, testCase.value)
			err := ValidateTarget(cfg)
			if err == nil || !strings.Contains(err.Error(), "production") {
				t.Fatalf("ValidateTarget() error = %v, want production refusal", err)
			}
		})
	}
}

func setDatabaseURL(cfg *config.Config, value string) { cfg.DatabaseURL = value }

func setAuditWriterDSN(cfg *config.Config, value string) { cfg.AuditWriterDSN = value }

func setAuditReaderDSN(cfg *config.Config, value string) { cfg.AuditReaderDSN = value }

func setAuditRedactorDSN(cfg *config.Config, value string) { cfg.AuditRedactorDSN = value }

func setAuditClickHouseDSN(cfg *config.Config, value string) {
	cfg.AuditClickHouseDSN = value
}

func setAuditKafkaBrokers(cfg *config.Config, value string) {
	cfg.AuditKafkaBrokers = value
}

func setMeiliURL(cfg *config.Config, value string) { cfg.MeiliURL = value }

func TestValidateTargetRejectsUnknownAllowTarget(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		DatabaseURL:        "postgres://user@yugabyte/tack",
		DatagenAllowTarget: "staging",
	}
	if err := ValidateTarget(cfg); err == nil {
		t.Fatal("ValidateTarget() error = nil, want unknown-marker refusal")
	}
}
