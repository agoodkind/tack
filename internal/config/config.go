// Package config loads server configuration from environment variables.
//
// The server does not load a configuration file. Supported secret environment
// variables may reference files that contain their values.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/caarlos0/env/v11"
)

type Config struct {
	DatabaseURL    string `env:"DATABASE_URL,required"`
	FDBClusterFile string `env:"FDB_CLUSTER_FILE" envDefault:"/etc/foundationdb/fdb.cluster"`
	// FDBTransactionTimeout bounds a product-store transaction, including retries.
	// During leader election, retries otherwise continue until the caller's context
	// ends; some callers omit a deadline (TACK-408).
	//
	// The original timeout rationale placed five seconds above the measured healthy
	// cross-guest commit time and below the ten-second guest-loss measurement
	// bound.
	FDBTransactionTimeout time.Duration `env:"FDB_TRANSACTION_TIMEOUT" envDefault:"5s"`
	Port                  int           `env:"PORT"                    envDefault:"8000"`
	Env                   string        `env:"ENV"                     envDefault:"development"`
	DatagenAllowTarget    string        `env:"TACK_DATAGEN_ALLOW_TARGET"`
	SearchEndpoint        string        `env:"OPENSEARCH_ENDPOINT"`
	SearchCA              string        `env:"OPENSEARCH_CA"`
	SearchUsername        string        `env:"OPENSEARCH_USERNAME"`
	SearchPassword        string        `env:"OPENSEARCH_PASSWORD"`
	SearchRequestTimeout  time.Duration `env:"OPENSEARCH_REQUEST_TIMEOUT" envDefault:"10s"`
	SearchMaxRetries      int           `env:"OPENSEARCH_MAX_RETRIES" envDefault:"2"`
	// SearchPublicEnabled enables ranked results for tack_search. When false,
	// tack_search returns its fixed unavailable response. Index workers run in
	// either state.
	SearchPublicEnabled bool `env:"OPENSEARCH_PUBLIC_ENABLED" envDefault:"false"`
	// RepairDir sets the repair directory for ops backfill once-node-integrity. The
	// command requires every request, manifest, and export file path to remain
	// inside this directory.
	RepairDir string `env:"TACK_REPAIR_DIR"`

	// These logging fields configure [goodkind.io/tack/internal/telemetry.Setup],
	// which creates gklog handlers without branching on ENV.
	//
	// LOG_JSON_FILE and LOG_TEXT_FILE select the file handlers. Empty paths use
	// $XDG_STATE_HOME/tack/logs, or ~/.local/state/tack/logs when XDG_STATE_HOME is
	// unset. A value of "-" disables only the corresponding handler.
	// LOG_DISABLE_FILES does not control these handlers.
	//
	// Deployment environment variables provide overrides. The local development
	// compose configuration sets LOG_MAX_BACKUPS=0 and LOG_MAX_AGE_DAYS=0 to retain
	// log history.
	//
	// LOG_LEVEL accepts "debug", "info", "warn", and "error"; an empty value uses
	// "debug". LOG_DISABLE_STDOUT disables the stdout JSON handler for processes
	// that reserve stdout for user output.
	LogLevel         string `env:"LOG_LEVEL"`
	LogJSONFile      string `env:"LOG_JSON_FILE"`
	LogTextFile      string `env:"LOG_TEXT_FILE"`
	LogDisableStdout bool   `env:"LOG_DISABLE_STDOUT"`
	LogMaxSizeMB     int    `env:"LOG_MAX_SIZE_MB"  envDefault:"100"`
	LogMaxBackups    int    `env:"LOG_MAX_BACKUPS"  envDefault:"0"`
	LogMaxAgeDays    int    `env:"LOG_MAX_AGE_DAYS" envDefault:"0"`
	// LogCommandDir enables separate JSON and text log files for command processes.
	// When empty, command processes use LOG_JSON_FILE and LOG_TEXT_FILE.
	LogCommandDir string `env:"LOG_COMMAND_DIR"`

	// Only ./server seed uses these seed settings.
	SeedEmail         string `env:"SEED_EMAIL"`
	SeedName          string `env:"SEED_NAME"`
	SeedOrgName       string `env:"SEED_ORG_NAME"       envDefault:"My Org"`
	SeedOrgSlug       string `env:"SEED_ORG_SLUG"       envDefault:"my-org"`
	SeedWorkspaceName string `env:"SEED_WORKSPACE_NAME" envDefault:"Main"`
	SeedWorkspaceSlug string `env:"SEED_WORKSPACE_SLUG" envDefault:"main"`
	// The seed command uses SeedAPIToken as the raw API token when set. Otherwise,
	// it generates a random token and prints it once.
	SeedAPIToken string `env:"SEED_API_TOKEN"`

	// Each app instance caches accepted tokens and organization memberships for the
	// configured lifetimes (TACK-504, TACK-505). Cache hits avoid the corresponding
	// database reads. Other instances may retain a revoked token or an old
	// membership until that entry expires. A zero lifetime disables the
	// corresponding cache.
	AuthTokenCacheLifetime      time.Duration `env:"AUTH_TOKEN_CACHE_LIFETIME"      envDefault:"30s"`
	AuthTokenCacheSize          int           `env:"AUTH_TOKEN_CACHE_SIZE"          envDefault:"65536"`
	AuthMembershipCacheLifetime time.Duration `env:"AUTH_MEMBERSHIP_CACHE_LIFETIME" envDefault:"30s"`
	AuthMembershipCacheSize     int           `env:"AUTH_MEMBERSHIP_CACHE_SIZE"     envDefault:"65536"`
	// AuthUserCacheLifetime and AuthUserCacheSize bound the user records that tool
	// output displays by ID. Other instances may display an old name until the
	// cached record expires.
	AuthUserCacheLifetime time.Duration `env:"AUTH_USER_CACHE_LIFETIME" envDefault:"5m"`
	AuthUserCacheSize     int           `env:"AUTH_USER_CACHE_SIZE"     envDefault:"65536"`

	// Separate audit roles use separate connection strings, or DSNs. The
	// application pool uses DATABASE_URL without audit privileges. AuditReaderDSN
	// supports export, and AuditRedactorDSN supports GDPR redaction (TACK-177,
	// TACK-178).
	//
	// The runtime selects Kafka when brokers are configured and otherwise uses
	// AuditWriterDSN for synchronous recording. Required audit operations must
	// refuse when recording or identity fails; an empty DSN does not waive that
	// requirement.
	AuditWriterDSN   string `env:"AUDIT_WRITER_DSN"`
	AuditReaderDSN   string `env:"AUDIT_READER_DSN"`
	AuditRedactorDSN string `env:"AUDIT_REDACTOR_DSN"`
	AuditOperatorDSN string `env:"AUDIT_OPERATOR_DSN"`

	// The ./server ops audit seed-roles command uses these passwords to create or
	// rotate the LOGIN roles for the audit DSNs. [Load] parses these fields with
	// the other service settings. Normal connections authenticate with the
	// passwords in their DSNs.
	AuditWriterPassword   string `env:"AUDIT_WRITER_PASSWORD"`
	AuditReaderPassword   string `env:"AUDIT_READER_PASSWORD"`
	AuditRedactorPassword string `env:"AUDIT_REDACTOR_PASSWORD"`
	AuditOperatorPassword string `env:"AUDIT_OPERATOR_PASSWORD"`
	// AppPassword supplies the password that seed-roles sets on tack_app, the
	// application's non-superuser DATABASE_URL login (TACK-180). Per-host
	// generation was the original deployment convention. [Load] parses it with the
	// other service settings.
	AppPassword string `env:"TACK_APP_PASSWORD"`
	// MigratorPassword supplies the password that seed-roles sets on tack_migrator
	// (TACK-554).
	MigratorPassword string `env:"TACK_MIGRATOR_PASSWORD"`

	// AuditAllowUnrecorded permits the runtime's discarding recorder when neither
	// Kafka brokers nor an audit writer DSN is configured. The runtime still
	// refuses a configured backend that fails.
	//
	// Earlier startup paths silently discarded events for both missing and broken
	// backends while serving requests. This flag distinguishes the missing-backend
	// case. The original production convention specified false. The binding audit
	// contract forbids unaudited user-data operations; this implementation path
	// does not satisfy that requirement.
	AuditAllowUnrecorded bool `env:"AUDIT_ALLOW_UNRECORDED"`

	// AuditBrokerReadyTimeout bounds startup retries while the ledger transport
	// starts. Compose and reboot cold starts start the broker alongside the app.
	// TACK-453 removed the provisioning failure in which a single unsuccessful
	// attempt repeatedly restarted a healthy deployment. Startup refuses a broker
	// that does not answer before the budget ends.
	AuditBrokerReadyTimeout time.Duration `env:"AUDIT_BROKER_READY_TIMEOUT" envDefault:"60s"`

	// AuditSigningKeyPath selects the PEM-encoded Ed25519 private key that the
	// notarizer uses to sign per-organization Merkle roots. An empty path disables
	// the notarizer.
	//
	// This command generates the key:
	//
	// 	./server gen-audit-key /etc/tack/audit-signing.pem
	AuditSigningKeyPath string `env:"AUDIT_SIGNING_KEY_PATH"`

	// AuditValidSigners contains accepted signing-key identifiers, separated by
	// commas (TACK-437). The audit signers command refuses an empty set, and audit
	// verify rejects a manifest with a signer outside the set.
	// [goodkind.io/tack/internal/audit.KeyIdentifier] derives the identifier that
	// audit.notarizations.signing_key stores.
	AuditValidSigners string `env:"AUDIT_VALID_SIGNERS"`

	// AuditKafkaBrokers lists Kafka bootstrap brokers, separated by commas. When
	// the list is empty, the runtime selects synchronous Yugabyte recording only if
	// AuditWriterDSN is set; otherwise, startup uses the missing-backend path.
	//
	// [Load] applies the declared defaults for AuditKafkaTopic, AuditKafkaClientID,
	// and AuditKafkaProduceTimeout when unset. These defaults implement the audit
	// producer design. The producer uses process-wide settings without a per-tenant
	// override.
	AuditKafkaBrokers        string        `env:"AUDIT_KAFKA_BROKERS"`
	AuditKafkaTopic          string        `env:"AUDIT_KAFKA_TOPIC"           envDefault:"audit.events.v1"`
	AuditKafkaClientID       string        `env:"AUDIT_KAFKA_CLIENT_ID"       envDefault:"tack-audit-producer"`
	AuditKafkaProduceTimeout time.Duration `env:"AUDIT_KAFKA_PRODUCE_TIMEOUT" envDefault:"15s"`

	// The app batches queued read-class audit events after requests (TACK-506).
	// AuditReadBufferCapacity bounds queued and retained events before overflow
	// uses the outbox. AuditReadBatchSize bounds each delivery chunk.
	// AuditReadFlushInterval sets the flush timer; backend failures and retries can
	// delay delivery beyond it.
	//
	// The recorder handles auth.token_used outside the ordinary read queue. It
	// appends the event to the overflow outbox when present; otherwise, it calls
	// the inner recorder synchronously. Under AuditAllowUnrecorded, the runtime can
	// omit the outbox and select [goodkind.io/tack/internal/audit.NoopRecorder],
	// which discards the event. The binding audit contract requires durable
	// admission and completed evidence before returning user data; the buffer
	// settings do not establish that guarantee for every read.
	AuditReadBufferCapacity int           `env:"AUDIT_READ_BUFFER_CAPACITY" envDefault:"8192"`
	AuditReadBatchSize      int           `env:"AUDIT_READ_BATCH_SIZE"      envDefault:"256"`
	AuditReadFlushInterval  time.Duration `env:"AUDIT_READ_FLUSH_INTERVAL"  envDefault:"200ms"`

	// An empty OTELEndpoint disables OpenTelemetry trace export.
	OTELEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	// Backup subcommands initialize storage and recovery schedules, export
	// snapshots, rehearse restores, and start continuous FoundationDB backups. The
	// original CT 117 production layout informed these defaults.
	BackupRoot       string `env:"TACK_BACKUP_ROOT"        envDefault:"/root/backups"`
	BackupFDBNetwork string `env:"TACK_BACKUP_FDB_NETWORK" envDefault:"tack_default"`
	BackupFDBImage   string `env:"TACK_BACKUP_FDB_IMAGE"   envDefault:"foundationdb/foundationdb:7.4.6"`

	// BackupFDBContinuous enables FoundationDB restore drills and staleness checks.
	// The ops provision command uses it to start continuous backup without a
	// separate manual command. The ops backup fdb-continuous-init command supports
	// a deliberate separate start and refuses when this flag is false.
	//
	// BackupFDBSnapshotInterval sets the interval in seconds for fdbbackup start
	// --snapshot-interval. The [FoundationDB backup reference] explains this
	// interval.
	//
	// [FoundationDB backup reference]: https://apple.github.io/foundationdb/backups.html
	BackupFDBContinuous       bool `env:"TACK_BACKUP_FDB_CONTINUOUS"        envDefault:"false"`
	BackupFDBSnapshotInterval int  `env:"TACK_BACKUP_FDB_SNAPSHOT_INTERVAL" envDefault:"3600"`

	// The ./server ops backup buckets-init command creates the SeaweedFS bucket for
	// off-host backup artifacts. The endpoint and credentials do not have defaults:
	// selecting the wrong object store can silently store backups at the wrong
	// destination. The command rejects a missing endpoint or credentials. The S3
	// client always uses the path-style addressing that SeaweedFS requires.
	BackupS3Endpoint   string `env:"TACK_BACKUP_S3_ENDPOINT"`
	BackupS3AccessKey  string `env:"TACK_BACKUP_S3_ACCESS_KEY_ID"`
	BackupS3SecretKey  string `env:"TACK_BACKUP_S3_SECRET_ACCESS_KEY"`
	BackupS3Region     string `env:"TACK_BACKUP_S3_REGION"        envDefault:"us-east-1"`
	BackupS3BucketMain string `env:"TACK_BACKUP_S3_BUCKET_MAIN"   envDefault:"tack-backups"`

	// The ./server ops backup yb-pitr-init command creates a yb-admin snapshot
	// schedule for the authentication and audit YSQL database. The schedule
	// supports recovery within its retention window. Interval and retention use
	// minutes, matching yb-admin create_snapshot_schedule.
	//
	// BackupYBImage selects the ledger engine image for yb-admin, ysql_dump,
	// ysql_dumpall, and temporary yugabyted restore containers. Docker Compose
	// supplies the yugabyte service's pinned image to tack-ops. Dumps and restore
	// rehearsals use the server's version.
	//
	// The field does not have a compiled default. An earlier default selected
	// 2025.2 for a 2024.2 cluster, and the restore drill failed while applying a
	// schema statement that the 2025.2 dumper emitted for the 2024.2 extension set.
	// Commands that start containers from BackupYBImage reject an empty value.
	BackupYBImage                string `env:"TACK_BACKUP_YB_IMAGE"`
	BackupYBMasterAddresses      string `env:"TACK_BACKUP_YB_MASTER_ADDRESSES"         envDefault:"yugabyte:7100"`
	BackupYBPITRIntervalMinutes  int    `env:"TACK_BACKUP_YB_PITR_INTERVAL_MINUTES"    envDefault:"60"`
	BackupYBPITRRetentionMinutes int    `env:"TACK_BACKUP_YB_PITR_RETENTION_MINUTES"   envDefault:"10080"`

	// LedgerTLSEnabled indicates that ledger nodes encrypt traffic and reject
	// unencrypted connections (TACK-460). LedgerCertsDir contains the issuing
	// certificate authority and each node's certificate. The original deployment
	// convention rendered both settings on every host.
	//
	// Database pools use the sslmode and authority path in their DSNs. Engine
	// administration uses the encrypted cluster protocol instead of SQL and
	// requires a certificate-directory flag. Backup and snapshot export connections
	// to an encrypted cluster fail without these settings.
	LedgerTLSEnabled bool   `env:"TACK_LEDGER_TLS_ENABLED" envDefault:"false"`
	LedgerCertsDir   string `env:"TACK_LEDGER_CERTS_DIR"`
	// LedgerNodeHosts pairs each permanent ledger node name with its pinned guest
	// address, using comma-separated name=address pairs. The original deployment
	// convention used the same mapping in ledger containers' hosts files.
	//
	// Encrypted SQL dump containers use this mapping because the engine image's
	// client library verifies certificate names rather than addresses. A connection
	// by address fails that verification. The map can remain empty for unencrypted
	// access or access through the local service name.
	LedgerNodeHosts string `env:"TACK_LEDGER_NODE_HOSTS"`

	// YugabyteDB snapshot export and restore require deployment-specific and
	// image-specific paths through environment variables. These fields do not have
	// defaults because a wrong path can read or write the wrong location.
	//
	// BackupYBRocksDBDir selects the RocksDB root inside the yugabyte container.
	// The single-node yugabyted layout uses
	// <base_dir>/data/yb-data/tserver/data/rocksdb, with per-tablet
	// .snapshots/<snapshot_id> directories.
	//
	// BackupYBOverlayPath selects the host path of the patched yugabyted binary
	// from upstream PR #23158. The restore drill mounts it into the temporary
	// container for startup on the IPv6-only bridge.
	BackupYBRocksDBDir  string `env:"TACK_BACKUP_YB_ROCKSDB_DIR"`
	BackupYBOverlayPath string `env:"TACK_BACKUP_YB_OVERLAY_PATH"`

	// BackupFDBOverlayPath selects the host path of fdb.bash with the IPv6
	// bracket-handling patch. The restore drill mounts the same overlay as the live
	// fdb service to start temporary FoundationDB containers on the IPv6-only
	// bridge. The deployment-specific path does not have a default.
	BackupFDBOverlayPath string `env:"TACK_BACKUP_FDB_OVERLAY_PATH"`

	// The ./server ops backup staleness-check command reports how long ago each
	// mechanism last succeeded. It compares each success age with its threshold in
	// seconds and exits nonzero when an age exceeds its threshold. The alarm policy
	// mails a new fault once.
	//
	// The thresholds allow scheduling margin as follows:
	//
	//   - Ledger export allows 36 hours for a daily export.
	//   - Restore rehearsal allows 8 days for a daily drill, matching the freshness
	//     acceptance bound with a week of margin.
	//   - Replication health allows 30 minutes for the probe that this check runs.
	//   - The FoundationDB restorable point allows 2 hours against the default
	//     one-hour snapshot interval.
	//
	// The 36-hour export threshold does not permit a complete missed daily run,
	// which requires a 48-hour success age.
	BackupStalenessExportMaxSeconds      int `env:"TACK_BACKUP_STALENESS_EXPORT_MAX_SECONDS"      envDefault:"129600"`
	BackupStalenessRehearsalMaxSeconds   int `env:"TACK_BACKUP_STALENESS_REHEARSAL_MAX_SECONDS"   envDefault:"691200"`
	BackupStalenessReplicationMaxSeconds int `env:"TACK_BACKUP_STALENESS_REPLICATION_MAX_SECONDS" envDefault:"1800"`
	BackupStalenessFDBMaxSeconds         int `env:"TACK_BACKUP_STALENESS_FDB_MAX_SECONDS"         envDefault:"7200"`

	// The staleness alarm mails each newly stale mechanism once, and every stale
	// run exits nonzero. Alarm memory uses a local JSON file under BackupRoot and a
	// shared object-store copy. BackupAlarmEmail selects the recipient. An empty
	// recipient suppresses mail, logs the omission, and does not mark the fault as
	// mailed; a later configured recipient can receive it.
	//
	// BackupAlarmMsmtprcPath selects the msmtp-format account file for host, port,
	// and credentials. The mailer implements SMTP directly. The container needs the
	// mounted account file without an msmtp executable.
	//
	// BackupAlarmPrimaryService selects the primary checker's --operator-service
	// value. The original owner's service used tack-backup. An empty value makes
	// this checker primary. A deputy suppresses new-fault mail when audit evidence
	// confirms a primary staleness-check within BackupAlarmPrimaryWindowSeconds. A
	// failed audit read allows the deputy to mail under the ordinary transition
	// policy.
	//
	// The original scheduling rationale used ten-minute timer intervals and a
	// 60-second random delay. The default 1500-second window covers two such
	// intervals plus five minutes. If the first audit read does not find a fresh
	// primary run, the deputy waits BackupAlarmPrimaryGraceSeconds before a second
	// read. The default 90-second grace exceeds that random delay to avoid
	// duplicate transition mail after a deployment or pause.
	BackupAlarmEmail                string `env:"TACK_BACKUP_ALARM_EMAIL"`
	BackupAlarmMsmtprcPath          string `env:"TACK_BACKUP_ALARM_MSMTPRC" envDefault:"/etc/msmtprc"`
	BackupAlarmPrimaryService       string `env:"TACK_BACKUP_ALARM_PRIMARY_SERVICE"`
	BackupAlarmPrimaryWindowSeconds int    `env:"TACK_BACKUP_ALARM_PRIMARY_WINDOW_SECONDS" envDefault:"1500"`
	BackupAlarmPrimaryGraceSeconds  int    `env:"TACK_BACKUP_ALARM_PRIMARY_GRACE_SECONDS"  envDefault:"90"`

	// YugabyteDB selects the database that backup dumps read.
	YugabyteDB string `env:"YUGABYTE_DB" envDefault:"tack"`

	// These fields date to the Wave 1, Phase 2 audit consumer configuration. The
	// standalone consumer now loads [AuditConsumerSettings] rather than these
	// [Config] fields. The app server does not consume them. Both loaders accept
	// the corresponding names from a shared environment.
	AuditConsumerKafkaBrokers   string        `env:"AUDIT_CONSUMER_KAFKA_BROKERS"`
	AuditConsumerKafkaTopic     string        `env:"AUDIT_CONSUMER_KAFKA_TOPIC"      envDefault:"audit.events.v1"`
	AuditConsumerGroupID        string        `env:"AUDIT_CONSUMER_GROUP_ID"         envDefault:"tack-audit-projector"`
	AuditConsumerBatchSize      int           `env:"AUDIT_CONSUMER_BATCH_SIZE"       envDefault:"256"`
	AuditConsumerPollInterval   time.Duration `env:"AUDIT_CONSUMER_POLL_INTERVAL"    envDefault:"250ms"`
	AuditConsumerYugabyteDSN    string        `env:"AUDIT_CONSUMER_YUGABYTE_DSN"`
	AuditConsumerSigningKeyPath string        `env:"AUDIT_CONSUMER_SIGNING_KEY_PATH"`

	// AuditConsumerLagWarnMessages specifies the per-partition threshold for
	// consumer.lag.high on each poll. The default is 1000 messages; the standalone
	// consumer reads the corresponding [AuditConsumerSettings.LagWarnMessages]
	// field.
	AuditConsumerLagWarnMessages int64 `env:"TACK_AUDIT_CONSUMER_LAG_WARN_MESSAGES" envDefault:"1000"`

	// AuditConsumerSummaryEvery specifies the number of records between
	// consumer.processed debug summaries. The default is 100; the standalone
	// consumer reads [AuditConsumerSettings.SummaryEvery].
	AuditConsumerSummaryEvery int `env:"TACK_AUDIT_CONSUMER_SUMMARY_EVERY" envDefault:"100"`

	// The ./server ops deploy verify command checks containers against these
	// expected images. DeployRegistry selects the registry namespace for
	// tack-server and tack-audit-consumer builds. DeployImageTag selects the tag
	// that Docker Compose uses from the deploy's TACK_IMAGE_TAG environment
	// variable.
	DeployRegistry string `env:"TACK_DEPLOY_REGISTRY" envDefault:"ghcr.io/agoodkind"`
	DeployImageTag string `env:"TACK_IMAGE_TAG"       envDefault:"latest"`
	// DeployComposeProject selects the deployed stack's Docker Compose project.
	// Deploy verification finds every app and audit-consumer replica by project and
	// service labels.
	DeployComposeProject string `env:"TACK_DEPLOY_COMPOSE_PROJECT" envDefault:"tack"`

	// The ./server ops provision command uses these container names through the
	// Docker socket. The host-networked tack-ops container cannot resolve bridge
	// DNS names. Defaults use the tack Compose project and the
	// <project>-<service>-1 naming format.
	OpsFDBContainer string `env:"TACK_OPS_FDB_CONTAINER" envDefault:"tack-fdb-1"`
	// OpsFDBRedundancyMode selects the mode that ops provision configures on a new
	// store and ops store set-redundancy uses without an argument. Local and
	// single-guest stores use single. The original deployment context rendered
	// double for environments with one process per data guest to restore replicated
	// storage after a rebuild (TACK-408).
	//
	// The commands accept only single, double, and triple because they insert the
	// mode as a word in an fdbcli command.
	OpsFDBRedundancyMode string `env:"TACK_OPS_FDB_REDUNDANCY_MODE" envDefault:"single"`
	OpsAppContainer      string `env:"TACK_OPS_APP_CONTAINER" envDefault:"tack-app-1"`
	// OpsFDBClusterDir selects the host directory that contains fdb.cluster. The
	// fdbcli containers mount it with read-write access at /etc/foundationdb;
	// fdbbackup containers mount it read-only at that path. [Load] rejects a
	// relative path because Docker would interpret it as a named volume.
	OpsFDBClusterDir string `env:"TACK_OPS_FDB_CLUSTER_DIR" envDefault:"/etc/foundationdb"`
}

func Load() (*Config, error) {
	environment, err := secretEnvironment(serviceSecretNames)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := env.ParseWithOptions(&cfg, env.Options{Environment: environment}); err != nil {
		wrapped := fmt.Errorf("parse server environment: %w", err)
		slog.Error("config.load_failed", slog.String("err", wrapped.Error()))
		return nil, wrapped
	}
	if !filepath.IsAbs(cfg.OpsFDBClusterDir) {
		err := fmt.Errorf("TACK_OPS_FDB_CLUSTER_DIR %q is not an absolute path", cfg.OpsFDBClusterDir)
		slog.Error("config.load_failed", slog.String("err", err.Error()))
		return nil, err
	}
	logsDir := xdgStatePath("tack", "logs")
	if cfg.LogJSONFile == "" {
		cfg.LogJSONFile = filepath.Join(logsDir, "app.jsonl")
	}
	if cfg.LogTextFile == "" {
		cfg.LogTextFile = filepath.Join(logsDir, "app.log")
	}
	// A "-" path disables the corresponding handler instead of using its XDG
	// default. Load converts it to the empty path that gklog treats as disabled.
	if cfg.LogJSONFile == "-" {
		cfg.LogJSONFile = ""
	}
	if cfg.LogTextFile == "-" {
		cfg.LogTextFile = ""
	}
	return &cfg, nil
}

// xdgStatePath joins elem under $XDG_STATE_HOME, or under $HOME/.local/state
// when XDG_STATE_HOME is empty. Logs use the XDG state directory rather than
// the configuration or cache directory.
func xdgStatePath(elem ...string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		base = filepath.Join(os.Getenv("HOME"), ".local", "state")
	}
	return filepath.Join(append([]string{base}, elem...)...)
}
