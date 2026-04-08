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
