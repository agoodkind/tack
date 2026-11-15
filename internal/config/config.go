// Package config loads server configuration from environment variables.
// The server does not read configuration files.
package config

import "time"

// Config defines the server and operator environment settings.
type Config struct {
	DatabaseURL    string `env:"DATABASE_URL,required"`
	FDBClusterFile string `env:"FDB_CLUSTER_FILE"      envDefault:"/etc/foundationdb/fdb.cluster"`
	// FDBTransactionTimeout bounds a transaction and all its retries,
	// including retries during leader elections (TACK-408).
	FDBTransactionTimeout time.Duration `env:"FDB_TRANSACTION_TIMEOUT"    envDefault:"5s"`
	Port                  int           `env:"PORT"                       envDefault:"8000"`
	Env                   string        `env:"ENV"                        envDefault:"development"`
	DatagenAllowTarget    string        `env:"TACK_DATAGEN_ALLOW_TARGET"`
	SearchEndpoint        string        `env:"OPENSEARCH_ENDPOINT"`
	SearchCA              string        `env:"OPENSEARCH_CA"`
	SearchUsername        string        `env:"OPENSEARCH_USERNAME"`
	SearchPassword        string        `env:"OPENSEARCH_PASSWORD"`
	SearchRequestTimeout  time.Duration `env:"OPENSEARCH_REQUEST_TIMEOUT" envDefault:"10s"`
	SearchMaxRetries      int           `env:"OPENSEARCH_MAX_RETRIES"     envDefault:"2"`
	// SearchPublicEnabled enables ranked tack_search results. Index workers
	// run regardless of this setting.
	SearchPublicEnabled bool `env:"OPENSEARCH_PUBLIC_ENABLED" envDefault:"false"`
	// RepairDir restricts every request, manifest, and export path for
	// ops backfill once-node-integrity to this directory.
	RepairDir string `env:"TACK_REPAIR_DIR"`

	// telemetry.Setup passes these settings to gklog without branching on ENV.
	// Empty log paths use the XDG state directory. A path of "-" disables its
	// file handler. Zero backup and age limits retain append-only history.
	LogLevel         string `env:"LOG_LEVEL"`
	LogJSONFile      string `env:"LOG_JSON_FILE"`
	LogTextFile      string `env:"LOG_TEXT_FILE"`
	LogDisableStdout bool   `env:"LOG_DISABLE_STDOUT"`
	LogMaxSizeMB     int    `env:"LOG_MAX_SIZE_MB"    envDefault:"100"`
	LogMaxBackups    int    `env:"LOG_MAX_BACKUPS"    envDefault:"0"`
	LogMaxAgeDays    int    `env:"LOG_MAX_AGE_DAYS"   envDefault:"0"`
	// LogCommandDir enables separate command logs. An empty value selects
	// LOG_JSON_FILE and LOG_TEXT_FILE instead.
	LogCommandDir string `env:"LOG_COMMAND_DIR"`

	// The seed command reads these initial identity and address settings.
	SeedEmail         string `env:"SEED_EMAIL"`
	SeedName          string `env:"SEED_NAME"`
	SeedOrgName       string `env:"SEED_ORG_NAME"       envDefault:"My Org"`
	SeedOrgSlug       string `env:"SEED_ORG_SLUG"       envDefault:"my-org"`
	SeedWorkspaceName string `env:"SEED_WORKSPACE_NAME" envDefault:"Main"`
	SeedWorkspaceSlug string `env:"SEED_WORKSPACE_SLUG" envDefault:"main"`
	// SeedAPIToken supplies a raw seed token. An empty value makes the seed
	// command generate a random token and print it once.
	SeedAPIToken string `env:"SEED_API_TOKEN"`

	// Each app instance caches accepted tokens and memberships for these
	// lifetimes. Other processes' revocations and membership changes take
	// effect after expiration. A zero lifetime disables the cache.
	AuthTokenCacheLifetime      time.Duration `env:"AUTH_TOKEN_CACHE_LIFETIME"      envDefault:"30s"`
	AuthTokenCacheSize          int           `env:"AUTH_TOKEN_CACHE_SIZE"          envDefault:"65536"`
	AuthMembershipCacheLifetime time.Duration `env:"AUTH_MEMBERSHIP_CACHE_LIFETIME" envDefault:"30s"`
	AuthMembershipCacheSize     int           `env:"AUTH_MEMBERSHIP_CACHE_SIZE"     envDefault:"65536"`
	// AuthUserCacheLifetime bounds cached display names in tool output.
	AuthUserCacheLifetime time.Duration `env:"AUTH_USER_CACHE_LIFETIME" envDefault:"5m"`
	AuthUserCacheSize     int           `env:"AUTH_USER_CACHE_SIZE"     envDefault:"65536"`

	// Audit roles use separate connection strings from the application role.
	// Empty values disable their corresponding subsystems.
	AuditWriterDSN   string `env:"AUDIT_WRITER_DSN"`
	AuditReaderDSN   string `env:"AUDIT_READER_DSN"`
	AuditRedactorDSN string `env:"AUDIT_REDACTOR_DSN"`
	AuditOperatorDSN string `env:"AUDIT_OPERATOR_DSN"`
	// Only ops audit seed-roles reads role passwords. Normal connections
	// authenticate with the credentials in their connection strings.
	AuditWriterPassword   string `env:"AUDIT_WRITER_PASSWORD"`
	AuditReaderPassword   string `env:"AUDIT_READER_PASSWORD"`
	AuditRedactorPassword string `env:"AUDIT_REDACTOR_PASSWORD"`
	AuditOperatorPassword string `env:"AUDIT_OPERATOR_PASSWORD"`
	AppPassword           string `env:"TACK_APP_PASSWORD"`
	MigratorPassword      string `env:"TACK_MIGRATOR_PASSWORD"`
	// AuditAllowUnrecorded explicitly permits operation without a ledger.
	// Production leaves this false; recording failures otherwise stop startup.
	AuditAllowUnrecorded bool `env:"AUDIT_ALLOW_UNRECORDED"`
	// AuditBrokerReadyTimeout permits broker startup retries during deploy
	// and reboot. Startup fails when this budget expires (TACK-453).
	AuditBrokerReadyTimeout time.Duration `env:"AUDIT_BROKER_READY_TIMEOUT" envDefault:"60s"`
	// AuditSigningKeyPath selects a PEM Ed25519 key for notarization.
	// An empty path disables notarization. gen-audit-key generates the key.
	AuditSigningKeyPath string `env:"AUDIT_SIGNING_KEY_PATH"`
	// AuditValidSigners lists comma-separated audit.KeyIdentifier values.
	// Verification rejects manifests from signers outside this list.
	AuditValidSigners string `env:"AUDIT_VALID_SIGNERS"`
	// Empty AuditKafkaBrokers selects synchronous Yugabyte audit recording.
	AuditKafkaBrokers        string        `env:"AUDIT_KAFKA_BROKERS"`
	AuditKafkaTopic          string        `env:"AUDIT_KAFKA_TOPIC"           envDefault:"audit.events.v1"`
	AuditKafkaClientID       string        `env:"AUDIT_KAFKA_CLIENT_ID"       envDefault:"tack-audit-producer"`
	AuditKafkaProduceTimeout time.Duration `env:"AUDIT_KAFKA_PRODUCE_TIMEOUT" envDefault:"15s"`
	// Read events use a bounded queue and batch flushes. Queue overflow uses
	// the outbox. The flush interval schedules delivery attempts; retries
	// can delay completion beyond that interval.
	AuditReadBufferCapacity int           `env:"AUDIT_READ_BUFFER_CAPACITY" envDefault:"8192"`
	AuditReadBatchSize      int           `env:"AUDIT_READ_BATCH_SIZE"      envDefault:"256"`
	AuditReadFlushInterval  time.Duration `env:"AUDIT_READ_FLUSH_INTERVAL"  envDefault:"200ms"`
	// An empty OTELEndpoint disables tracing.
	OTELEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	BackupRoot       string `env:"TACK_BACKUP_ROOT"        envDefault:"/root/backups"`
	BackupFDBNetwork string `env:"TACK_BACKUP_FDB_NETWORK" envDefault:"tack_default"`
	BackupFDBImage   string `env:"TACK_BACKUP_FDB_IMAGE"   envDefault:"foundationdb/foundationdb:7.4.6"`
	// BackupFDBContinuous enables continuous backup during provision,
	// restore drills, and staleness checks. Manual initialization requires it.
	BackupFDBContinuous       bool `env:"TACK_BACKUP_FDB_CONTINUOUS"        envDefault:"false"`
	BackupFDBSnapshotInterval int  `env:"TACK_BACKUP_FDB_SNAPSHOT_INTERVAL" envDefault:"3600"`
	// S3 initialization requires an explicit endpoint and credentials.
	// SeaweedFS clients always use path-style addressing.
	BackupS3Endpoint   string `env:"TACK_BACKUP_S3_ENDPOINT"`
	BackupS3AccessKey  string `env:"TACK_BACKUP_S3_ACCESS_KEY_ID"`
	BackupS3SecretKey  string `env:"TACK_BACKUP_S3_SECRET_ACCESS_KEY"`
	BackupS3Region     string `env:"TACK_BACKUP_S3_REGION"            envDefault:"us-east-1"`
	BackupS3BucketMain string `env:"TACK_BACKUP_S3_BUCKET_MAIN"       envDefault:"tack-backups"`
	// BackupYBImage must match the deployed engine for SQL dumps, yb-admin,
	// and restore drills. Commands reject an empty value rather than choosing
	// a different engine version. PITR intervals and retention use minutes.
	BackupYBImage                string `env:"TACK_BACKUP_YB_IMAGE"`
	BackupYBMasterAddresses      string `env:"TACK_BACKUP_YB_MASTER_ADDRESSES"       envDefault:"yugabyte:7100"`
	BackupYBPITRIntervalMinutes  int    `env:"TACK_BACKUP_YB_PITR_INTERVAL_MINUTES"  envDefault:"60"`
	BackupYBPITRRetentionMinutes int    `env:"TACK_BACKUP_YB_PITR_RETENTION_MINUTES" envDefault:"10080"`
	// Engine administration uses these TLS settings. SQL pools instead read
	// sslmode and certificate paths from their connection strings.
	LedgerTLSEnabled bool   `env:"TACK_LEDGER_TLS_ENABLED" envDefault:"false"`
	LedgerCertsDir   string `env:"TACK_LEDGER_CERTS_DIR"`
	// LedgerNodeHosts maps certificate hostnames to guest addresses with
	// comma-separated name=address pairs for encrypted SQL dump connections.
	LedgerNodeHosts string `env:"TACK_LEDGER_NODE_HOSTS"`
	// Restore commands require explicit engine data and overlay paths.
	// BackupYBRocksDBDir contains per-tablet .snapshots directories.
	// Engine overlays support the IPv6-only bridge during restore drills.
	BackupYBRocksDBDir   string `env:"TACK_BACKUP_YB_ROCKSDB_DIR"`
	BackupYBOverlayPath  string `env:"TACK_BACKUP_YB_OVERLAY_PATH"`
	BackupFDBOverlayPath string `env:"TACK_BACKUP_FDB_OVERLAY_PATH"`
	// Staleness checks compare artifact ages with these second-based limits.
	// Defaults tolerate a missed scheduled run.
	BackupStalenessExportMaxSeconds      int `env:"TACK_BACKUP_STALENESS_EXPORT_MAX_SECONDS"      envDefault:"129600"`
	BackupStalenessRehearsalMaxSeconds   int `env:"TACK_BACKUP_STALENESS_REHEARSAL_MAX_SECONDS"   envDefault:"691200"`
	BackupStalenessReplicationMaxSeconds int `env:"TACK_BACKUP_STALENESS_REPLICATION_MAX_SECONDS" envDefault:"1800"`
	BackupStalenessFDBMaxSeconds         int `env:"TACK_BACKUP_STALENESS_FDB_MAX_SECONDS"         envDefault:"7200"`
	// A stale mechanism triggers one email per transition and a nonzero exit.
	// An empty recipient logs the skipped email without saving alarm state.
	// BackupAlarmMsmtprcPath supplies SMTP settings; an msmtp binary is unnecessary.
	BackupAlarmEmail       string `env:"TACK_BACKUP_ALARM_EMAIL"`
	BackupAlarmMsmtprcPath string `env:"TACK_BACKUP_ALARM_MSMTPRC" envDefault:"/etc/msmtprc"`
	// A deputy mails only without a recent primary audit event, or when its
	// ledger read fails. The grace interval permits one retry after startup.
	BackupAlarmPrimaryService       string `env:"TACK_BACKUP_ALARM_PRIMARY_SERVICE"`
	BackupAlarmPrimaryWindowSeconds int    `env:"TACK_BACKUP_ALARM_PRIMARY_WINDOW_SECONDS" envDefault:"1500"`
	BackupAlarmPrimaryGraceSeconds  int    `env:"TACK_BACKUP_ALARM_PRIMARY_GRACE_SECONDS"  envDefault:"90"`
	YugabyteDB                      string `env:"YUGABYTE_DB"                              envDefault:"tack"`

	// The audit-consumer binary reads these settings from the shared environment.
	AuditConsumerKafkaBrokers string `env:"AUDIT_CONSUMER_KAFKA_BROKERS"`
	AuditConsumerKafkaTopic   string `env:"AUDIT_CONSUMER_KAFKA_TOPIC"   envDefault:"audit.events.v1"`

	AuditConsumerGroupID string `env:"AUDIT_CONSUMER_GROUP_ID" envDefault:"tack-audit-projector"`

	AuditConsumerBatchSize       int           `env:"AUDIT_CONSUMER_BATCH_SIZE"             envDefault:"256"`
	AuditConsumerPollInterval    time.Duration `env:"AUDIT_CONSUMER_POLL_INTERVAL"          envDefault:"250ms"`
	AuditConsumerYugabyteDSN     string        `env:"AUDIT_CONSUMER_YUGABYTE_DSN"`
	AuditConsumerSigningKeyPath  string        `env:"AUDIT_CONSUMER_SIGNING_KEY_PATH"`
	AuditConsumerLagWarnMessages int64         `env:"TACK_AUDIT_CONSUMER_LAG_WARN_MESSAGES" envDefault:"1000"`
	AuditConsumerSummaryEvery    int           `env:"TACK_AUDIT_CONSUMER_SUMMARY_EVERY"     envDefault:"100"`
	// Deploy verification compares every app and audit-consumer replica with
	// the requested image registry, tag, and Compose project labels.
	DeployRegistry       string `env:"TACK_DEPLOY_REGISTRY"        envDefault:"ghcr.io/agoodkind"`
	DeployImageTag       string `env:"TACK_IMAGE_TAG"              envDefault:"latest"`
	DeployComposeProject string `env:"TACK_DEPLOY_COMPOSE_PROJECT" envDefault:"tack"`
	// Provision accesses these containers with the Docker socket because
	// host-networked tack-ops cannot resolve bridge service hostnames.
	OpsFDBContainer string `env:"TACK_OPS_FDB_CONTAINER" envDefault:"tack-fdb-1"`
	// OpsFDBRedundancyMode accepts single, double, or triple for initial
	// provisioning and ops store set-redundancy (TACK-408).
	OpsFDBRedundancyMode string `env:"TACK_OPS_FDB_REDUNDANCY_MODE" envDefault:"single"`
	OpsAppContainer      string `env:"TACK_OPS_APP_CONTAINER"       envDefault:"tack-app-1"`
	// OpsFDBClusterDir requires an absolute host directory for fdb.cluster.
	// fdbcli mounts it read-write; fdbbackup mounts it read-only.
	OpsFDBClusterDir string `env:"TACK_OPS_FDB_CLUSTER_DIR" envDefault:"/etc/foundationdb"`
}
