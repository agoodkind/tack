// Package config loads server configuration from environment variables.
// All configuration comes from env vars; there is no config file.
package config

import (
	"os"
	"path/filepath"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	DatabaseURL    string `env:"DATABASE_URL,required"`
	FDBClusterFile string `env:"FDB_CLUSTER_FILE" envDefault:"/etc/foundationdb/fdb.cluster"`
	Port           int    `env:"PORT"             envDefault:"8000"`
	Env            string `env:"ENV"              envDefault:"development"`

	// Logging. Every field is plain pass-through to telemetry.Setup, which
	// hands them to gklog. Setup itself never branches on ENV.
	//
	// LOG_JSON_FILE and LOG_TEXT_FILE point at on-disk handlers. Empty
	// values fall back to XDG-conformant defaults: $XDG_STATE_HOME/tack/logs
	// (or ~/.local/state/tack/logs when XDG_STATE_HOME is unset). Set
	// LOG_JSON_FILE=- (or LOG_DISABLE_FILES=1) to suppress on-disk output
	// entirely. The deploy env (docker-compose, systemd, .env) is the
	// canonical place to override; in our local dev compose we set
	// LOG_MAX_BACKUPS=0 LOG_MAX_AGE_DAYS=0 so logs are append-only history.
	//
	// LOG_LEVEL accepts "debug", "info", "warn", "error". Empty defers to
	// gklog's default (debug).
	//
	// LOG_DISABLE_STDOUT silences the stdout JSON handler. Useful when a
	// caller process treats stdout as user-facing output.
	LogLevel         string `env:"LOG_LEVEL"`
	LogJSONFile      string `env:"LOG_JSON_FILE"`
	LogTextFile      string `env:"LOG_TEXT_FILE"`
	LogDisableStdout bool   `env:"LOG_DISABLE_STDOUT"`
	LogMaxSizeMB     int    `env:"LOG_MAX_SIZE_MB"  envDefault:"100"`
	LogMaxBackups    int    `env:"LOG_MAX_BACKUPS"  envDefault:"0"`
	LogMaxAgeDays    int    `env:"LOG_MAX_AGE_DAYS" envDefault:"0"`

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

	// Audit ledger pools. Each role connects through its own DSN so the app
	// pool (DATABASE_URL) cannot accidentally inherit audit privileges.
	// Empty values disable the corresponding subsystem. AuditWriterDSN is
	// the only one required for the production Recorder; reader and redactor
	// are used by export and GDPR redaction (TACK-177, TACK-178).
	AuditWriterDSN   string `env:"AUDIT_WRITER_DSN"`
	AuditReaderDSN   string `env:"AUDIT_READER_DSN"`
	AuditRedactorDSN string `env:"AUDIT_REDACTOR_DSN"`

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
	logsDir := xdgStatePath("tack", "logs")
	if cfg.LogJSONFile == "" {
		cfg.LogJSONFile = filepath.Join(logsDir, "app.jsonl")
	}
	if cfg.LogTextFile == "" {
		cfg.LogTextFile = filepath.Join(logsDir, "app.log")
	}
	// "-" is the explicit way to disable a handler that would otherwise
	// take the XDG default. gklog treats empty paths as disabled, so we
	// translate "-" to empty here.
	if cfg.LogJSONFile == "-" {
		cfg.LogJSONFile = ""
	}
	if cfg.LogTextFile == "-" {
		cfg.LogTextFile = ""
	}
	return &cfg, nil
}

// xdgStatePath returns "$XDG_STATE_HOME/<elem...>" with a fallback to
// "$HOME/.local/state/<elem...>". Tack's logs are state, not config or
// cache, so XDG_STATE_HOME is the right anchor.
func xdgStatePath(elem ...string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(append([]string{base}, elem...)...)
}
