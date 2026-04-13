// Package config loads server configuration from environment variables.
// All configuration comes from env vars; there is no config file.
package config

import "github.com/caarlos0/env/v11"

type Config struct {
	DatabaseURL    string `env:"DATABASE_URL,required"`
	FDBClusterFile string `env:"FDB_CLUSTER_FILE" envDefault:"/etc/foundationdb/fdb.cluster"`
	Port           int    `env:"PORT"             envDefault:"8000"`
	Env            string `env:"ENV"              envDefault:"development"`

	// Logging
	LogLevel      string `env:"LOG_LEVEL"        envDefault:"info"`
	LogFile       string `env:"LOG_FILE"`                         // empty = stdout only
	LogMaxSizeMB  int    `env:"LOG_MAX_SIZE_MB"  envDefault:"100"` // rotate at 100 MB
	LogMaxBackups int    `env:"LOG_MAX_BACKUPS"  envDefault:"0"`   // 0 = unlimited
	LogMaxAgeDays int    `env:"LOG_MAX_AGE_DAYS" envDefault:"0"`   // 0 = unlimited

	// Seed: used by `./server seed` only.
	SeedEmail         string `env:"SEED_EMAIL"`
	SeedName          string `env:"SEED_NAME"`
	SeedOrgName       string `env:"SEED_ORG_NAME"       envDefault:"My Org"`
	SeedOrgSlug       string `env:"SEED_ORG_SLUG"       envDefault:"my-org"`
	SeedWorkspaceName string `env:"SEED_WORKSPACE_NAME" envDefault:"Main"`
	SeedWorkspaceSlug string `env:"SEED_WORKSPACE_SLUG" envDefault:"main"`
	// If set, the seed command uses this as the raw API token.
	// If unset, a random token is generated and printed once.
	SeedAPIToken string `env:"SEED_API_TOKEN"`

	// Meilisearch: optional, no-op stub used when unset.
	MeiliURL       string `env:"MEILI_URL"        envDefault:"http://localhost:7700"`
	MeiliMasterKey string `env:"MEILI_MASTER_KEY" envDefault:"tack-dev-meili-key-change-in-prod"`

	// Temporal: background workflow engine. Defaults to localhost for dev.
	TemporalAddress string `env:"TEMPORAL_ADDRESS" envDefault:"localhost:7233"`

	// Optional: if unset, OTEL tracing is a no-op.
	OTELEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
