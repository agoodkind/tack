package datagen

import (
	"fmt"
	"log/slog"
	"net/netip"
	"net/url"
	"strings"

	"goodkind.io/tack/internal/config"
)

const (
	productionHostname = "tack.home.goodkind.io"
	productionIPv6     = "3d06:bad:b01::117"
)

var productionAddress = netip.MustParseAddr(productionIPv6)

// ValidateTarget requires an explicit QA or local write marker.
func ValidateTarget(cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("qa datagen: configuration is required")
	}
	endpoints := []targetEndpoint{
		{name: "DATABASE_URL", value: cfg.DatabaseURL},
		{name: "AUDIT_WRITER_DSN", value: cfg.AuditWriterDSN},
		{name: "AUDIT_READER_DSN", value: cfg.AuditReaderDSN},
		{name: "AUDIT_REDACTOR_DSN", value: cfg.AuditRedactorDSN},
		{name: "AUDIT_CLICKHOUSE_DSN", value: cfg.AuditClickHouseDSN},
		{name: "MEILI_URL", value: cfg.MeiliURL},
	}
	for _, endpoint := range endpoints {
		if err := validateEndpoint(endpoint); err != nil {
			return err
		}
	}
	for _, broker := range strings.Split(cfg.AuditKafkaBrokers, ",") {
		if err := validateEndpoint(targetEndpoint{
			name: "AUDIT_KAFKA_BROKERS", value: strings.TrimSpace(broker), kafka: true,
		}); err != nil {
			return err
		}
	}
	allowTarget := strings.ToLower(strings.TrimSpace(cfg.DatagenAllowTarget))
	if allowTarget == "qa" || allowTarget == "local" {
		return nil
	}
	return fmt.Errorf(
		"qa datagen refused: TACK_DATAGEN_ALLOW_TARGET must be exactly qa or local, got %q",
		cfg.DatagenAllowTarget,
	)
}

type targetEndpoint struct {
	name  string
	value string
	kafka bool
}

func validateEndpoint(endpoint targetEndpoint) error {
	if strings.TrimSpace(endpoint.value) == "" {
		return nil
	}
	host, err := endpointHost(endpoint)
	if err != nil {
		return err
	}
	address, addressErr := netip.ParseAddr(host)
	if addressErr == nil && address.Unmap() == productionAddress.Unmap() {
		return fmt.Errorf("qa datagen refused: %s identifies production", endpoint.name)
	}
	if addressErr != nil && strings.Contains(host, productionHostname) {
		return fmt.Errorf("qa datagen refused: %s identifies production", endpoint.name)
	}
	return nil
}

func endpointHost(endpoint targetEndpoint) (string, error) {
	value := endpoint.value
	if endpoint.kafka {
		value = "kafka://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil {
		slog.Error("qa.datagen.failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("qa datagen: parse %s: %w", endpoint.name, err)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "" {
		return "", fmt.Errorf("qa datagen: %s host is required", endpoint.name)
	}
	return host, nil
}
