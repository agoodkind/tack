package config_test

import (
	"encoding/base64"
	"os"
	"strings"
	"testing"

	"goodkind.io/tack/internal/config"
)

func TestLoadSearchQuerySettingsReadsCursorKeyFile(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	t.Setenv("OPENSEARCH_CURSOR_KEY", "")
	t.Setenv("OPENSEARCH_CURSOR_KEY_FILE", writeSecret(t, key+"\n"))
	settings, err := config.LoadSearchQuerySettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.CursorKey != key || os.Getenv("OPENSEARCH_CURSOR_KEY") != "" {
		t.Fatal("the cursor key did not remain confined to configuration")
	}
}

func TestLoadSearchMaintenanceCredentialsRequiresSeparateProductionFile(t *testing.T) {
	t.Setenv("ENV", "production")
	t.Setenv("OPENSEARCH_USERNAME", "runtime-fixture")
	t.Setenv("OPENSEARCH_PASSWORD", "runtime-fixture-password")
	t.Setenv("OPENSEARCH_MAINTENANCE_USERNAME", "maintenance-fixture")
	t.Setenv("OPENSEARCH_MAINTENANCE_PASSWORD", "")
	t.Setenv("OPENSEARCH_MAINTENANCE_PASSWORD_FILE", writeSecret(t, "maintenance-fixture-password\n"))
	credentials, err := config.LoadSearchMaintenanceCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if credentials.Username != "maintenance-fixture" || credentials.Password != "maintenance-fixture-password" {
		t.Fatal("maintenance credentials did not use the separate file")
	}
	if os.Getenv("OPENSEARCH_MAINTENANCE_PASSWORD") != "" {
		t.Fatal("maintenance configuration exported its secret")
	}
	t.Setenv("OPENSEARCH_MAINTENANCE_PASSWORD_FILE", "")
	if _, err := config.LoadSearchMaintenanceCredentials(); err == nil {
		t.Fatal("maintenance configuration reused runtime credentials")
	}
}

func TestLoadAuditConsumerSettingsReadsDSNFiles(t *testing.T) {
	t.Setenv("AUDIT_CONSUMER_KAFKA_BROKERS", "local-fixture:9092")
	t.Setenv("ENV", "development")
	t.Setenv("AUDIT_CONSUMER_YUGABYTE_DSN", "")
	t.Setenv("AUDIT_CONSUMER_YUGABYTE_DSN_FILE", writeSecret(t, "postgres://consumer-fixture"))
	t.Setenv("AUDIT_CONSUMER_CLICKHOUSE_DSN", "")
	t.Setenv("AUDIT_CONSUMER_CLICKHOUSE_DSN_FILE", writeSecret(t, "clickhouse://consumer-fixture"))
	settings, err := config.LoadAuditConsumerSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.YugabyteDSN != "postgres://consumer-fixture" || settings.ClickHouseDSN != "clickhouse://consumer-fixture" {
		t.Fatal("consumer configuration did not resolve its DSN files")
	}
}
