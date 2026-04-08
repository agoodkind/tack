package config

import (
	"github.com/caarlos0/env/v11"
	"github.com/google/uuid"
)

type Config struct {
	DatabaseURL    string    `env:"DATABASE_URL,required"`
	FDBClusterFile string    `env:"FDB_CLUSTER_FILE" envDefault:"/etc/foundationdb/fdb.cluster"`
	OrgID          uuid.UUID `env:"ORG_ID,required"`
	Port           int       `env:"PORT"             envDefault:"8000"`
	Env            string    `env:"ENV"              envDefault:"development"`
	LogLevel       string    `env:"LOG_LEVEL"        envDefault:"info"`
	// Optional — if unset, OTEL tracing is a no-op.
	OTELEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}
