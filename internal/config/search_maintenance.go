package config

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/caarlos0/env/v11"
)

// SearchMaintenanceCredentials supplies authentication for search operator
// commands. Application configuration does not load these credentials.
type SearchMaintenanceCredentials struct {
	Username string `env:"OPENSEARCH_MAINTENANCE_USERNAME"`
	Password string `env:"OPENSEARCH_MAINTENANCE_PASSWORD"`
}

// LoadSearchMaintenanceCredentials reads separate credentials for search
// operator commands.
//
// LoadSearchMaintenanceCredentials resolves the password reference and rejects
// empty credentials. Production requires a password file reference.
//
// The credentials require these settings:
//
//   - OPENSEARCH_MAINTENANCE_USERNAME supplies the username.
//   - OPENSEARCH_MAINTENANCE_PASSWORD_FILE supplies the production password
//     path.
//   - OPENSEARCH_MAINTENANCE_PASSWORD can supply the password outside
//     production.
func LoadSearchMaintenanceCredentials() (SearchMaintenanceCredentials, error) {
	environment, err := secretEnvironment([]string{"OPENSEARCH_MAINTENANCE_PASSWORD"})
	if err != nil {
		return SearchMaintenanceCredentials{}, err
	}

	if environment["ENV"] == "production" &&
		environment["OPENSEARCH_MAINTENANCE_PASSWORD_FILE"] == "" {
		return SearchMaintenanceCredentials{}, errors.New(
			"OPENSEARCH_MAINTENANCE_PASSWORD_FILE is required in production",
		)
	}

	var credentials SearchMaintenanceCredentials
	if err := env.ParseWithOptions(
		&credentials,
		env.Options{Environment: environment},
	); err != nil {
		wrapped := fmt.Errorf("load search maintenance credentials: %w", err)
		slog.Error("config.search_maintenance_failed", slog.String("err", wrapped.Error()))

		return credentials, wrapped
	}

	if credentials.Username == "" || credentials.Password == "" {
		return SearchMaintenanceCredentials{}, errors.New(
			"OPENSEARCH_MAINTENANCE_USERNAME and OPENSEARCH_MAINTENANCE_PASSWORD_FILE are required",
		)
	}

	return credentials, nil
}
