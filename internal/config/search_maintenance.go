package config

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/caarlos0/env/v11"
)

// SearchMaintenanceCredentials authenticates search operator commands. The
// application configuration does not load these credentials.
type SearchMaintenanceCredentials struct {
	Username string `env:"OPENSEARCH_MAINTENANCE_USERNAME"`
	Password string `env:"OPENSEARCH_MAINTENANCE_PASSWORD"`
}

// LoadSearchMaintenanceCredentials reads credentials only when an operator
// command needs them. Production requires a password file reference.
func LoadSearchMaintenanceCredentials() (SearchMaintenanceCredentials, error) {
	environment, err := secretEnvironment([]string{"OPENSEARCH_MAINTENANCE_PASSWORD"})
	if err != nil {
		return SearchMaintenanceCredentials{}, err
	}
	if environment["ENV"] == "production" && environment["OPENSEARCH_MAINTENANCE_PASSWORD_FILE"] == "" {
		return SearchMaintenanceCredentials{}, errors.New("OPENSEARCH_MAINTENANCE_PASSWORD_FILE is required in production")
	}
	var credentials SearchMaintenanceCredentials
	if err := env.ParseWithOptions(&credentials, env.Options{Environment: environment}); err != nil {
		wrapped := fmt.Errorf("load search maintenance credentials: %w", err)
		slog.Error("config.search_maintenance_failed", slog.String("err", wrapped.Error()))
		return credentials, wrapped
	}
	if credentials.Username == "" || credentials.Password == "" {
		return SearchMaintenanceCredentials{}, errors.New("OPENSEARCH_MAINTENANCE_USERNAME and OPENSEARCH_MAINTENANCE_PASSWORD_FILE are required")
	}
	return credentials, nil
}
