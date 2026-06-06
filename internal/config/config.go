// Package config loads server configuration from environment variables.
// All configuration comes from env vars; there is no config file.
package config

import (
	"os"
	"path/filepath"
	"time"

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

	// Audit role passwords. Read only by `./server ops audit seed-roles`,
	// which creates or rotates the LOGIN roles the DSNs above authenticate as.
	// The running server never reads these; for normal connections the
	// password travels inside the DSN.
	AuditWriterPassword   string `env:"AUDIT_WRITER_PASSWORD"`
	AuditReaderPassword   string `env:"AUDIT_READER_PASSWORD"`
	AuditRedactorPassword string `env:"AUDIT_REDACTOR_PASSWORD"`

	// AuditSigningKeyPath points at a PEM-encoded Ed25519 private key used
	// by the notarizer to sign per-org Merkle roots. Generate with:
	//   ./server gen-audit-key /etc/tack/audit-signing.pem
	// Empty disables the notarizer.
	AuditSigningKeyPath string `env:"AUDIT_SIGNING_KEY_PATH"`

	// WAL directory for read-class audit events. Empty falls back to
	// $XDG_STATE_HOME/tack/audit-wal (typically /var/lib/tack/audit-wal in
	// the production container). Set to "-" to disable the WAL entirely
	// and bypass the read audit path.
	AuditWALDir string `env:"AUDIT_WAL_DIR"`

	// WAL backlog observability tuning. All are optional; absence keeps the
	// defaults wired in NewWALRecorder.
	//
	// AuditWALMaxBacklogSegments: number of non-active segments above which
	// the backlog signal flips for telemetry. Default 64. Observational only.
	//
	// AuditWALMaxBacklogAge: age of the oldest non-active segment above
	// which the backlog signal flips for telemetry. Default 10m.
	// Observational only.
	//
	// AuditWALIdleRotateAfter: how long the active segment may sit idle
	// before the drainer force-rotates it. Default 500ms (2x drain interval).
	AuditWALMaxBacklogSegments int32         `env:"AUDIT_WAL_MAX_BACKLOG_SEGMENTS" envDefault:"0"`
	AuditWALMaxBacklogAge      time.Duration `env:"AUDIT_WAL_MAX_BACKLOG_AGE"      envDefault:"0"`
	AuditWALIdleRotateAfter    time.Duration `env:"AUDIT_WAL_IDLE_ROTATE_AFTER"    envDefault:"0"`

	// Kafka audit producer (Wave 1 of the Phase 2 audit refactor).
	//
	// AuditKafkaBrokers is a comma-separated bootstrap broker list. When
	// empty, the Kafka producer path is disabled and audit recording falls
	// back to the WAL-only path.
	//
	// AuditKafkaTopic, AuditKafkaClientID, and AuditKafkaProduceTimeout fall
	// back to design-doc defaults when unset. The producer code reads them
	// verbatim; there is no per-tenant override.
	AuditKafkaBrokers        string        `env:"AUDIT_KAFKA_BROKERS"`
	AuditKafkaTopic          string        `env:"AUDIT_KAFKA_TOPIC"           envDefault:"audit.events.v1"`
	AuditKafkaClientID       string        `env:"AUDIT_KAFKA_CLIENT_ID"       envDefault:"tack-audit-producer"`
	AuditKafkaProduceTimeout time.Duration `env:"AUDIT_KAFKA_PRODUCE_TIMEOUT" envDefault:"10s"`

	// Meilisearch: optional, no-op stub used when unset.
	MeiliURL       string `env:"MEILI_URL"        envDefault:"http://localhost:7700"`
	MeiliMasterKey string `env:"MEILI_MASTER_KEY" envDefault:"tack-dev-meili-key-change-in-prod"`

	// Temporal: background workflow engine. Defaults to localhost for dev.
	TemporalAddress string `env:"TEMPORAL_ADDRESS" envDefault:"localhost:7233"`

	// Optional: if unset, OTEL tracing is a no-op.
	OTELEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	// Backup family. Used by `./server ops backup [...]`. Defaults match the
	// production layout on CT 117. The Temporal-DB password has no default
	// because losing the dump silently is the failure mode this whole family
	// exists to prevent; the backup command errors loudly if it is unset.
	BackupRoot              string `env:"TACK_BACKUP_ROOT"               envDefault:"/root/backups"`
	BackupSnapshotRoot      string `env:"TACK_BACKUP_SNAPSHOT_ROOT"      envDefault:"/root/fdb-snapshots"`
	BackupFDBNetwork        string `env:"TACK_BACKUP_FDB_NETWORK"        envDefault:"tack_default"`
	BackupFDBImage          string `env:"TACK_BACKUP_FDB_IMAGE"          envDefault:"foundationdb/foundationdb:7.4.6"`
	BackupFDBTimeoutSeconds int    `env:"TACK_BACKUP_FDB_TIMEOUT_SECONDS" envDefault:"1800"`
	BackupFDBSidecar        string `env:"TACK_BACKUP_FDB_SIDECAR"        envDefault:"tack-backup-agent"`

	// FDB continuous backup. When BackupFDBContinuous is true, the FDB step
	// drives a continuous `fdbbackup start` that streams straight to the
	// SeaweedFS object store via a blobstore:// destination built from the
	// BackupS3* fields, instead of the one-shot file:///snapshot path that
	// tars its result. BackupFDBSnapshotInterval is the snapshot interval in
	// seconds passed to `fdbbackup start --snapshot_interval`. See
	// https://apple.github.io/foundationdb/backups.html
	BackupFDBContinuous       bool `env:"TACK_BACKUP_FDB_CONTINUOUS"        envDefault:"false"`
	BackupFDBSnapshotInterval int  `env:"TACK_BACKUP_FDB_SNAPSHOT_INTERVAL" envDefault:"3600"`

	BackupMeiliVolume    string `env:"TACK_BACKUP_MEILI_VOLUME"       envDefault:"tack_meili-data"`
	BackupTemporalDBUser string `env:"TACK_BACKUP_TEMPORAL_DB_USER"   envDefault:"temporal"`
	BackupTemporalDBPass string `env:"TACK_BACKUP_TEMPORAL_DB_PASSWORD"`
	BackupTemporalDBName string `env:"TACK_BACKUP_TEMPORAL_DB_NAME"   envDefault:"temporal"`
	BackupDockerContext  string `env:"TACK_BACKUP_DOCKER_CONTEXT"     envDefault:"tack"`

	// Backup S3 target. Read by `./server ops backup buckets-init` to create
	// the SeaweedFS buckets that hold off-host backup artifacts. Endpoint and
	// credentials have no defaults because pointing at the wrong object store
	// is a silent data-placement failure; the command errors loudly if the
	// endpoint or credentials are unset. SeaweedFS requires path-style
	// addressing, which the S3 client sets regardless of these values.
	BackupS3Endpoint    string `env:"TACK_BACKUP_S3_ENDPOINT"`
	BackupS3AccessKey   string `env:"TACK_BACKUP_S3_ACCESS_KEY_ID"`
	BackupS3SecretKey   string `env:"TACK_BACKUP_S3_SECRET_ACCESS_KEY"`
	BackupS3Region      string `env:"TACK_BACKUP_S3_REGION"        envDefault:"us-east-1"`
	BackupS3BucketMain  string `env:"TACK_BACKUP_S3_BUCKET_MAIN"   envDefault:"tack-backups"`
	BackupS3BucketAudit string `env:"TACK_BACKUP_S3_BUCKET_AUDIT"  envDefault:"tack-audit-archive"`

	// YugabyteDB point-in-time-recovery (PITR). Read by
	// `./server ops backup yb-pitr-init`, which creates a yb-admin snapshot
	// schedule over the auth + audit YSQL database so a fresh deploy can roll
	// back to any point within the retention window. The image tag must match
	// the live yugabyte service in docker-compose.yml so yb-admin's wire
	// protocol agrees with the running masters. Interval and retention are in
	// minutes, matching yb-admin create_snapshot_schedule's argument units.
	BackupYBPITRImage            string `env:"TACK_BACKUP_YB_IMAGE"                    envDefault:"yugabytedb/yugabyte:2025.2.3.0-b149"`
	BackupYBMasterAddresses      string `env:"TACK_BACKUP_YB_MASTER_ADDRESSES"         envDefault:"yugabyte:7100"`
	BackupYBPITRIntervalMinutes  int    `env:"TACK_BACKUP_YB_PITR_INTERVAL_MINUTES"    envDefault:"60"`
	BackupYBPITRRetentionMinutes int    `env:"TACK_BACKUP_YB_PITR_RETENTION_MINUTES"   envDefault:"10080"`

	// YugabyteDB distributed-snapshot export and restore. No defaults: these
	// are deployment- and image-specific paths that must be declared in .env so
	// a wrong value fails loudly rather than silently writing or reading the
	// wrong location. BackupYBRocksDBDir is the rocksdb root inside the yugabyte
	// container (on the yugabyted single-node layout,
	// <base_dir>/data/yb-data/tserver/data/rocksdb), where the per-tablet
	// `.snapshots/<snapshot_id>` directories live. BackupYBOverlayPath is the
	// host path to the patched yugabyted binary (PR #23158) the restore drill
	// bind-mounts into the throwaway yugabyted so it boots on the IPv6-only
	// bridge.
	BackupYBRocksDBDir  string `env:"TACK_BACKUP_YB_ROCKSDB_DIR"`
	BackupYBOverlayPath string `env:"TACK_BACKUP_YB_OVERLAY_PATH"`

	// BackupFDBOverlayPath is the host path to the patched fdb.bash (IPv6
	// bracket handling) the restore drill bind-mounts into the throwaway
	// FoundationDB so it boots on the IPv6-only bridge, the same overlay the
	// live fdb service mounts. No default, for the same reason as the YB paths.
	BackupFDBOverlayPath string `env:"TACK_BACKUP_FDB_OVERLAY_PATH"`

	// Yugabyte credentials. Read by the backup family for the ysql_dump call;
	// the live tack server reads YUGABYTE_PASSWORD via the DATABASE_URL DSN
	// instead, so these are only consulted by ops backup.
	YugabyteUser     string `env:"YUGABYTE_USER"     envDefault:"yugabyte"`
	YugabytePassword string `env:"YUGABYTE_PASSWORD"`
	YugabyteDB       string `env:"YUGABYTE_DB"       envDefault:"tack"`

	// Audit consumer (Wave 1, Phase 2). The audit-consumer binary reads
	// these. The tack-app server does not consume them today. They live
	// here so a single .env can drive both binaries.
	AuditConsumerKafkaBrokers   string        `env:"AUDIT_CONSUMER_KAFKA_BROKERS"`
	AuditConsumerKafkaTopic     string        `env:"AUDIT_CONSUMER_KAFKA_TOPIC"      envDefault:"audit.events.v1"`
	AuditConsumerGroupID        string        `env:"AUDIT_CONSUMER_GROUP_ID"         envDefault:"tack-audit-projector"`
	AuditConsumerBatchSize      int           `env:"AUDIT_CONSUMER_BATCH_SIZE"       envDefault:"256"`
	AuditConsumerPollInterval   time.Duration `env:"AUDIT_CONSUMER_POLL_INTERVAL"    envDefault:"250ms"`
	AuditConsumerYugabyteDSN    string        `env:"AUDIT_CONSUMER_YUGABYTE_DSN"`
	AuditConsumerClickHouseDSN  string        `env:"AUDIT_CONSUMER_CLICKHOUSE_DSN"`
	AuditConsumerSigningKeyPath string        `env:"AUDIT_CONSUMER_SIGNING_KEY_PATH"`

	// AuditConsumerLagWarnMessages is the per-partition lag threshold
	// above which the consumer logs `consumer.lag.high` on every poll.
	// Default 1000 messages.
	AuditConsumerLagWarnMessages int64 `env:"TACK_AUDIT_CONSUMER_LAG_WARN_MESSAGES" envDefault:"1000"`

	// AuditConsumerSummaryEvery is how many records between batched
	// `consumer.processed` debug summaries. Default 100.
	AuditConsumerSummaryEvery int `env:"TACK_AUDIT_CONSUMER_SUMMARY_EVERY" envDefault:"100"`

	// Deploy: ./server ops deploy reads these. Operator-facing only; the
	// running server never references them. Kept on Config for env-parity
	// with the rest of the binary so a single .env drives both the server
	// and the deploy subcommand.
	//
	// DeployRegistry is the image repository the SHA-tagged image is pushed
	// to in registry mode. DeployMode selects "registry" (default) or
	// "offline"; offline pipes ImageSave through ImageLoad over the docker
	// context. DeployDockerContext names the docker context that talks to
	// the production daemon over ssh://. DeployRemoteComposeFile is the
	// compose path on the remote. DeployTimeout is the overall deploy
	// timeout (build is the usual long pole).
	DeployRegistry          string `env:"TACK_DEPLOY_REGISTRY"            envDefault:"ghcr.io/agoodkind/tack"`
	DeployMode              string `env:"TACK_DEPLOY_MODE"                envDefault:"registry"`
	DeployDockerContext     string `env:"TACK_DEPLOY_DOCKER_CONTEXT"      envDefault:"tack"`
	DeployRemoteComposeFile string `env:"TACK_DEPLOY_REMOTE_COMPOSE_FILE" envDefault:"/root/tack/docker-compose.yml"`
	DeployTimeoutSeconds    int    `env:"TACK_DEPLOY_TIMEOUT_SECONDS"     envDefault:"600"`
}

func Load() (*Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, err
	}
	logsDir := xdgStatePath("tack", "logs")
	if cfg.AuditWALDir == "" {
		cfg.AuditWALDir = xdgStatePath("tack", "audit-wal")
	}
	if cfg.AuditWALDir == "-" {
		cfg.AuditWALDir = ""
	}
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
