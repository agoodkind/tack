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
	// FDBTransactionTimeout bounds one product-store transaction, its retries
	// included. A transaction issued while the store elects a new leader
	// otherwise retries until the caller's context ends, and not every caller
	// sets one (TACK-408). Five seconds sits above a healthy cross-guest commit
	// and below the ten-second bound a guest loss is measured against.
	FDBTransactionTimeout time.Duration `env:"FDB_TRANSACTION_TIMEOUT" envDefault:"5s"`
	Port                  int           `env:"PORT"                    envDefault:"8000"`
	Env                   string        `env:"ENV"                     envDefault:"development"`
	DatagenAllowTarget    string        `env:"TACK_DATAGEN_ALLOW_TARGET"`

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

	// Auth caches (TACK-504, TACK-505). An accepted token and a user's org
	// set are remembered in each app instance for the lifetime, so a repeat
	// request costs no ledger read. The lifetime is also how long a
	// revocation or a membership change made in another process takes to
	// reach every instance. A lifetime of 0 caches nothing.
	AuthTokenCacheLifetime      time.Duration `env:"AUTH_TOKEN_CACHE_LIFETIME"      envDefault:"30s"`
	AuthTokenCacheSize          int           `env:"AUTH_TOKEN_CACHE_SIZE"          envDefault:"65536"`
	AuthMembershipCacheLifetime time.Duration `env:"AUTH_MEMBERSHIP_CACHE_LIFETIME" envDefault:"30s"`
	AuthMembershipCacheSize     int           `env:"AUTH_MEMBERSHIP_CACHE_SIZE"     envDefault:"65536"`
	// AuthUserCache* bound the user records tool output renders by id; the
	// lifetime is how long a display-name change takes to reach every
	// instance.
	AuthUserCacheLifetime time.Duration `env:"AUTH_USER_CACHE_LIFETIME" envDefault:"5m"`
	AuthUserCacheSize     int           `env:"AUTH_USER_CACHE_SIZE"     envDefault:"65536"`

	// Audit ledger pools. Each role connects through its own DSN so the app
	// pool (DATABASE_URL) cannot accidentally inherit audit privileges.
	// Empty values disable the corresponding subsystem. AuditWriterDSN is
	// the only one required for the production Recorder; reader and redactor
	// are used by export and GDPR redaction (TACK-177, TACK-178).
	AuditWriterDSN   string `env:"AUDIT_WRITER_DSN"`
	AuditReaderDSN   string `env:"AUDIT_READER_DSN"`
	AuditRedactorDSN string `env:"AUDIT_REDACTOR_DSN"`
	AuditOperatorDSN string `env:"AUDIT_OPERATOR_DSN"`

	// Audit role passwords. Read only by `./server ops audit seed-roles`,
	// which creates or rotates the LOGIN roles the DSNs above authenticate as.
	// The running server never reads these; for normal connections the
	// password travels inside the DSN.
	AuditWriterPassword   string `env:"AUDIT_WRITER_PASSWORD"`
	AuditReaderPassword   string `env:"AUDIT_READER_PASSWORD"`
	AuditRedactorPassword string `env:"AUDIT_REDACTOR_PASSWORD"`
	AuditOperatorPassword string `env:"AUDIT_OPERATOR_PASSWORD"`
	// AppPassword is the password seed-roles sets on tack_app, the
	// non-superuser login the application's DATABASE_URL authenticates as
	// (TACK-180). The deploy generates it per host; the running server never
	// reads it.
	AppPassword string `env:"TACK_APP_PASSWORD"`

	// AuditAllowUnrecorded lets a deployment run with no ledger at all. It
	// exists because "no audit backend is configured" and "the audit backend
	// is broken" are different situations that used to look identical: both
	// silently substituted a recorder that discarded every event, and the
	// server served normally either way. The server now refuses to start
	// unless it can record, and this flag is the only way to say that
	// unrecorded operation is intended. Production leaves it false.
	AuditAllowUnrecorded bool `env:"AUDIT_ALLOW_UNRECORDED"`

	// AuditBrokerReadyTimeout is how long startup waits for the ledger
	// transport to answer before refusing. It is a budget rather than a single
	// attempt because a compose or reboot cold start brings the broker up
	// alongside the app: a first-attempt refusal there would crash-loop a
	// healthy deployment, which is the failure TACK-453 removed from the
	// provision path. A broker that is genuinely down still refuses, one
	// budget later.
	AuditBrokerReadyTimeout time.Duration `env:"AUDIT_BROKER_READY_TIMEOUT" envDefault:"60s"`

	// AuditSigningKeyPath points at a PEM-encoded Ed25519 private key used
	// by the notarizer to sign per-org Merkle roots. Generate with:
	//   ./server gen-audit-key /etc/tack/audit-signing.pem
	// Empty disables the notarizer.
	AuditSigningKeyPath string `env:"AUDIT_SIGNING_KEY_PATH"`

	// AuditValidSigners is the comma-separated set of signing-key identifiers
	// this environment accepts (TACK-437). `audit signers` refuses to run
	// without it, and `audit verify` rejects a manifest signed outside it.
	// Each entry is the identifier `audit.KeyIdentifier` derives, the string
	// audit.notarizations.signing_key holds.
	AuditValidSigners string `env:"AUDIT_VALID_SIGNERS"`

	// Kafka audit producer. AuditKafkaBrokers is a comma-separated bootstrap
	// broker list. When empty, the Kafka producer path is disabled and audit
	// recording uses the synchronous Yugabyte recorder.
	//
	// AuditKafkaTopic, AuditKafkaClientID, and AuditKafkaProduceTimeout fall
	// back to design-doc defaults when unset. The producer code reads them
	// verbatim; there is no per-tenant override.
	AuditKafkaBrokers        string        `env:"AUDIT_KAFKA_BROKERS"`
	AuditKafkaTopic          string        `env:"AUDIT_KAFKA_TOPIC"           envDefault:"audit.events.v1"`
	AuditKafkaClientID       string        `env:"AUDIT_KAFKA_CLIENT_ID"       envDefault:"tack-audit-producer"`
	AuditKafkaProduceTimeout time.Duration `env:"AUDIT_KAFKA_PRODUCE_TIMEOUT" envDefault:"15s"`

	// Read-class audit events are queued in the app and delivered in batches
	// behind the request (TACK-506). AuditReadBufferCapacity is the queue
	// depth past which an arriving event goes to the outbox instead;
	// AuditReadBatchSize is the most events one flush delivers;
	// AuditReadFlushInterval is the longest a queued event waits for a flush,
	// which is also the most a token's last use can lag its request.
	AuditReadBufferCapacity int           `env:"AUDIT_READ_BUFFER_CAPACITY" envDefault:"8192"`
	AuditReadBatchSize      int           `env:"AUDIT_READ_BATCH_SIZE"      envDefault:"256"`
	AuditReadFlushInterval  time.Duration `env:"AUDIT_READ_FLUSH_INTERVAL"  envDefault:"200ms"`

	// Meilisearch. Both values are required and may not be empty: a
	// compiled-in address or key would let a process start against the
	// wrong search engine, or with a key everyone knows, without saying so,
	// and an empty rendered value is the same silence (TACK-268).
	// docker-compose.yml sets the address per service and the key from .env.
	MeiliURL       string `env:"MEILI_URL,required,notEmpty"`
	MeiliMasterKey string `env:"MEILI_MASTER_KEY,required,notEmpty"`

	// Optional: if unset, OTEL tracing is a no-op.
	OTELEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`

	// Backup operations. The subcommands initialize backup storage and recovery
	// schedules, export snapshots, drill restores, and start continuous
	// FoundationDB backup. Defaults match the production layout on CT 117.
	BackupRoot       string `env:"TACK_BACKUP_ROOT"        envDefault:"/root/backups"`
	BackupFDBNetwork string `env:"TACK_BACKUP_FDB_NETWORK" envDefault:"tack_default"`
	BackupFDBImage   string `env:"TACK_BACKUP_FDB_IMAGE"   envDefault:"foundationdb/foundationdb:7.4.6"`

	// FDB continuous backup. BackupFDBContinuous enables the FDB legs of the
	// restore drill and the staleness check, and it is the switch `ops
	// provision` reads to start the continuous session unattended, so an
	// environment that sets it needs no operator to run
	// `ops backup fdb-continuous-init` by hand; that subcommand remains for a
	// deliberate out-of-band start and refuses to run when this is false.
	// BackupFDBSnapshotInterval is the snapshot interval in seconds passed to
	// `fdbbackup start --snapshot-interval`. See
	// https://apple.github.io/foundationdb/backups.html
	BackupFDBContinuous       bool `env:"TACK_BACKUP_FDB_CONTINUOUS"        envDefault:"false"`
	BackupFDBSnapshotInterval int  `env:"TACK_BACKUP_FDB_SNAPSHOT_INTERVAL" envDefault:"3600"`

	// Backup S3 target. Read by `./server ops backup buckets-init` to create
	// the SeaweedFS bucket that holds off-host backup artifacts. Endpoint and
	// credentials have no defaults because pointing at the wrong object store
	// is a silent data-placement failure; the command errors loudly if the
	// endpoint or credentials are unset. SeaweedFS requires path-style
	// addressing, which the S3 client sets regardless of these values.
	BackupS3Endpoint   string `env:"TACK_BACKUP_S3_ENDPOINT"`
	BackupS3AccessKey  string `env:"TACK_BACKUP_S3_ACCESS_KEY_ID"`
	BackupS3SecretKey  string `env:"TACK_BACKUP_S3_SECRET_ACCESS_KEY"`
	BackupS3Region     string `env:"TACK_BACKUP_S3_REGION"        envDefault:"us-east-1"`
	BackupS3BucketMain string `env:"TACK_BACKUP_S3_BUCKET_MAIN"   envDefault:"tack-backups"`

	// YugabyteDB point-in-time-recovery (PITR). Read by
	// `./server ops backup yb-pitr-init`, which creates a yb-admin snapshot
	// schedule over the auth + audit YSQL database so a fresh deploy can roll
	// back to any point within the retention window. Interval and retention
	// are in minutes, matching yb-admin create_snapshot_schedule's argument
	// units.
	//
	// BackupYBImage is the ledger's own engine image. Every yb-admin
	// one-shot, the ysql_dump and ysql_dumpall one-shots, and the restore
	// drill's throwaway yugabyted run from it, so the dumper matches the server
	// and a rehearsal restores into the version the cluster runs.
	// docker-compose.yml renders it for tack-ops from the yugabyte service's
	// pin. There is no compiled default because a default here is a second
	// home for the tag, and it drifted: the binary carried 2025.2 against a
	// 2024.2 cluster, and the drill's schema apply failed on a statement the
	// 2025.2 dumper wrote for the 2024.2 extension set. Every command that
	// runs a container from it refuses to start while it is empty.
	BackupYBImage                string `env:"TACK_BACKUP_YB_IMAGE"`
	BackupYBMasterAddresses      string `env:"TACK_BACKUP_YB_MASTER_ADDRESSES"         envDefault:"yugabyte:7100"`
	BackupYBPITRIntervalMinutes  int    `env:"TACK_BACKUP_YB_PITR_INTERVAL_MINUTES"    envDefault:"60"`
	BackupYBPITRRetentionMinutes int    `env:"TACK_BACKUP_YB_PITR_RETENTION_MINUTES"   envDefault:"10080"`

	// Ledger transport encryption (TACK-460). LedgerTLSEnabled says whether
	// this environment's ledger nodes encrypt their traffic and refuse an
	// unencrypted connection; LedgerCertsDir is the directory holding the
	// authority that issued them, which is also where each node keeps its own
	// certificate. The deploy renders both on every host.
	//
	// The database pools need nothing from these: their DSNs already carry
	// sslmode and the authority's path. They exist for the engine's own
	// administration tool, which speaks the encrypted cluster protocol rather
	// than SQL and takes its certificate directory as a flag, so a backup or a
	// snapshot export against an encrypted cluster fails to connect without
	// them.
	LedgerTLSEnabled bool   `env:"TACK_LEDGER_TLS_ENABLED" envDefault:"false"`
	LedgerCertsDir   string `env:"TACK_LEDGER_CERTS_DIR"`
	// LedgerNodeHosts maps each ledger node's permanent name to the pinned
	// address of the guest that runs it, as name=address pairs separated by
	// commas, the same map the deploy renders into every ledger container's
	// hosts file. The SQL dumpers need it under encryption: they run in the
	// engine's own image, whose client library verifies a certificate against
	// a name only, never an address, so a dump that dialed the address it
	// reached the node at would be refused by the certificate that names it.
	// Empty where the ledger runs in the clear or on the local service name.
	LedgerNodeHosts string `env:"TACK_LEDGER_NODE_HOSTS"`

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

	// Backup staleness thresholds, in seconds. `./server ops backup
	// staleness-check` reports how long ago each mechanism last succeeded and
	// exits nonzero once an age passes its threshold, mailing once when it
	// does. Every default leaves room for one missed run of the
	// schedule that feeds it, so a single transient failure is not an alert:
	// 36h over the daily ledger export, 8 days over the daily restore drill
	// rehearsal (the acceptance criterion's own freshness bound, which a daily
	// drill clears with a week of margin), 30m over the replication health
	// probe that runs with this check, and 2h over the FoundationDB restorable
	// point, whose default snapshot interval is one hour.
	BackupStalenessExportMaxSeconds      int `env:"TACK_BACKUP_STALENESS_EXPORT_MAX_SECONDS"      envDefault:"129600"`
	BackupStalenessRehearsalMaxSeconds   int `env:"TACK_BACKUP_STALENESS_REHEARSAL_MAX_SECONDS"   envDefault:"691200"`
	BackupStalenessReplicationMaxSeconds int `env:"TACK_BACKUP_STALENESS_REPLICATION_MAX_SECONDS" envDefault:"1800"`
	BackupStalenessFDBMaxSeconds         int `env:"TACK_BACKUP_STALENESS_FDB_MAX_SECONDS"         envDefault:"7200"`

	// Backup staleness alarm mail. The staleness-check run that first finds a
	// mechanism past its threshold mails a plain-words account of the fault,
	// once per mechanism (the memory is a JSON file under BackupRoot), and
	// every stale run still exits nonzero. BackupAlarmEmail is the recipient;
	// empty mails nothing and logs that it did not, which is how a local run
	// works with no mail configured, and records nothing, so the fault mails
	// when a recipient is set. BackupAlarmMsmtprcPath is the msmtp-format
	// account file the mailer parses for host, port, and credentials. It then
	// speaks SMTP itself, so a container needs that file mounted but no msmtp
	// binary.
	//
	// BackupAlarmPrimaryService is the --operator-service name the primary
	// checker runs under (the owner's unit uses tack-backup); empty means this
	// checker is the primary and mails on every transition. Set on a deputy,
	// which mails a fault only when the ledger holds no staleness-check event
	// recorded by that service within BackupAlarmPrimaryWindowSeconds. The
	// window covers two missed runs of the primary's timer plus five minutes,
	// so a single missed run does not hand the alarm to the deputy: 1500
	// seconds over a ten-minute timer. A deputy that cannot read the ledger
	// mails, which is the failure mode that loses least.
	// BackupAlarmPrimaryGraceSeconds is how long a deputy whose first ledger
	// read found no fresh primary run waits before reading once more, so two
	// timers that fire within seconds of each other after a deployment or a
	// pause do not both mail the same transition: 90 seconds covers the 60
	// seconds of random delay the timers carry.
	BackupAlarmEmail                string `env:"TACK_BACKUP_ALARM_EMAIL"`
	BackupAlarmMsmtprcPath          string `env:"TACK_BACKUP_ALARM_MSMTPRC" envDefault:"/etc/msmtprc"`
	BackupAlarmPrimaryService       string `env:"TACK_BACKUP_ALARM_PRIMARY_SERVICE"`
	BackupAlarmPrimaryWindowSeconds int    `env:"TACK_BACKUP_ALARM_PRIMARY_WINDOW_SECONDS" envDefault:"1500"`
	BackupAlarmPrimaryGraceSeconds  int    `env:"TACK_BACKUP_ALARM_PRIMARY_GRACE_SECONDS"  envDefault:"90"`

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

	// The audit topic's shape on a cluster the consumer creates it on
	// (TACK-409): how many copies of every partition, and how many of those
	// copies must be in sync before an acks=all produce is acknowledged.
	// Zero leaves both to the broker, which is the one-broker stack. An
	// existing topic keeps its copy count; `ops queue set-replication`
	// raises that.
	AuditConsumerTopicReplicationFactor int `env:"AUDIT_CONSUMER_TOPIC_REPLICATION_FACTOR"`
	AuditConsumerTopicMinInSyncReplicas int `env:"AUDIT_CONSUMER_TOPIC_MIN_INSYNC_REPLICAS"`

	// AuditConsumerLagWarnMessages is the per-partition lag threshold
	// above which the consumer logs `consumer.lag.high` on every poll.
	// Default 1000 messages.
	AuditConsumerLagWarnMessages int64 `env:"TACK_AUDIT_CONSUMER_LAG_WARN_MESSAGES" envDefault:"1000"`

	// AuditConsumerSummaryEvery is how many records between batched
	// `consumer.processed` debug summaries. Default 100.
	AuditConsumerSummaryEvery int `env:"TACK_AUDIT_CONSUMER_SUMMARY_EVERY" envDefault:"100"`

	// Deploy verification: `./server ops deploy verify` reads these to name
	// the images the rolled containers must run. DeployRegistry is the
	// registry namespace the build workflow pushes tack-server and
	// tack-audit-consumer under; DeployImageTag is the tag the compose file
	// pins them at, the same TACK_IMAGE_TAG the deploy renders into .env.
	DeployRegistry string `env:"TACK_DEPLOY_REGISTRY" envDefault:"ghcr.io/agoodkind"`
	DeployImageTag string `env:"TACK_IMAGE_TAG"       envDefault:"latest"`

	// Provision: `./server ops provision` reads these to reach the running fdb
	// and app containers through the Docker socket (tack-ops is host-networked
	// and cannot resolve the bridge DNS names). Defaults match the compose
	// project name "tack" (<project>-<service>-1).
	OpsFDBContainer string `env:"TACK_OPS_FDB_CONTAINER" envDefault:"tack-fdb-1"`
	// OpsFDBRedundancyMode is the redundancy `ops provision` configures on a
	// store that has never been configured, and the mode `ops store
	// set-redundancy` sets without an argument. single is the local and
	// single-guest value; an environment running one process per data guest
	// renders double, so a rebuild comes back replicated rather than with one
	// copy of every key (TACK-408). Only single, double, and triple are
	// accepted, because the value reaches fdbcli as a word in a command.
	OpsFDBRedundancyMode string `env:"TACK_OPS_FDB_REDUNDANCY_MODE" envDefault:"single"`
	OpsAppContainer      string `env:"TACK_OPS_APP_CONTAINER" envDefault:"tack-app-1"`
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
