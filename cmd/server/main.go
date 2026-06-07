package main

import (
	"context"
	"expvar"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	mcpadapter "goodkind.io/tack/internal/adapters/mcp"
	mcptools "goodkind.io/tack/internal/adapters/mcp/tools"
	"goodkind.io/tack/internal/adapters/postgres"
	searchadapter "goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auth"
	"goodkind.io/tack/internal/config"
	domainsearch "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
	"goodkind.io/tack/internal/version"
	"goodkind.io/tack/migrations"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}

	closer, err := telemetry.Setup(telemetry.LogConfig{
		Level:         cfg.LogLevel,
		JSONFile:      cfg.LogJSONFile,
		TextFile:      cfg.LogTextFile,
		DisableStdout: cfg.LogDisableStdout,
		MaxSizeMB:     cfg.LogMaxSizeMB,
		MaxBackups:    cfg.LogMaxBackups,
		MaxAgeDays:    cfg.LogMaxAgeDays,
		OTELEndpoint:  cfg.OTELEndpoint,
	})
	if err != nil {
		slog.Error("telemetry.setup", "err", err)
		os.Exit(1)
	}
	defer func() {
		if closer != nil {
			_ = closer.Close()
		}
	}()
	slog.Info("server.startup",
		slog.String("env", cfg.Env),
		slog.String("version", version.Tag()),
		slog.String("commit", version.Commit()),
	)

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "migrate":
			runMigrations(cfg)
			return
		case "seed":
			runSeed(cfg)
			return
		case "ops":
			runOps(cfg, os.Args[2:])
			return
		case "audit-export":
			runAuditExport(cfg, os.Args[2:])
			return
		case "audit-verify":
			runAuditVerify(os.Args[2:])
			return
		case "gen-audit-key":
			if len(os.Args) < 3 {
				slog.Error("gen-audit-key", "err", "usage: server gen-audit-key <output.pem>")
				fatalExit()
			}
			err := audit.GenerateAuditSigningKey(os.Args[2])
			if err != nil {
				slog.Error("gen-audit-key.failed")
				fatalExit()
			}
			slog.Info("gen-audit-key.completed")
			return
		}
	}

	runServer(cfg)
}

func runMigrations(cfg *config.Config) {
	ctx := context.Background()
	err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS)
	if err != nil {
		slog.Error("migrate", "err", err)
		fatalExit()
	}
	slog.Info("migrations complete")
}

func fatalExit() {
	os.Exit(1)
}

func runServer(cfg *config.Config) {
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.Error("postgres", "err", err)
		fatalExit()
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.Error("foundationdb", "err", err)
		fatalExit()
	}

	searcher := buildSearcher(cfg)

	auditRuntime := setupAuditRuntime(ctx, cfg)
	defer auditRuntime.Close()
	tokenRepo := postgres.NewTokenRepo(pool)
	userRepo := postgres.NewUserRepo(pool)
	orgMembers := postgres.NewOrgMemberRepo(pool)

	nodeSvc := service.NewNodeService(
		fdbStores.Nodes,
		fdbStores.Views,
		fdbStores.NodeTypes,
		fdbStores.PropertyDefs,
		fdbStores.Relationships,
		fdbStores.NodeDeleter,
		searcher,
	)

	mcpHandler := mcpadapter.NewHandler(mcpadapter.Deps{
		NodeSvc:       nodeSvc,
		Nodes:         fdbStores.Nodes,
		Reader:        fdbStores.Views,
		NodeTypes:     fdbStores.NodeTypes,
		PropertyDefs:  fdbStores.PropertyDefs,
		Relationships: fdbStores.Relationships,
		Members:       orgMembers,
		Users:         userRepo,
		Searcher:      searcher,
		AuditReader:   auditRuntime.Reader,
		AuditQuerier:  auditRuntime.Querier,
		AuditRedactor: auditRuntime.Redactor,
	})

	authMiddleware := buildAuthMiddleware(cfg, tokenRepo)

	mux := buildServeMux(mcpHandler, authMiddleware)

	addr := fmt.Sprintf(":%d", cfg.Port)
	slog.Info("starting server",
		"addr", addr,
		"env", cfg.Env,
		"version", version.Tag(),
		"commit", version.Commit(),
		"build_hash", version.BuildHash(),
		"build_time", version.BuildTime(),
		"dirty", version.Dirty(),
	)

	srv := &http.Server{
		Addr:              addr,
		Handler:           h2c.NewHandler(telemetry.RequestLogger(mux), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("http.panic", slog.Any("err", r))
			}
		}()
		err := srv.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			slog.Error("http", "err", err)
			fatalExit()
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = srv.Shutdown(shutCtx)
	if err != nil {
		slog.Error("shutdown", "err", err)
	}
}

type auditRuntime struct {
	Reader     *audit.Reader
	ClickHouse *audit.ClickHouseReader
	Querier    audit.RowQuerier
	Redactor   *audit.Redactor
	Notarizer  *audit.Notarizer
	Recorder   audit.Recorder
}

func setupAuditRuntime(ctx context.Context, cfg *config.Config) auditRuntime {
	auditRec := buildAuditRecorder(ctx, cfg)
	// Wrap once at startup so seed and other internal automation can mute
	// emission via audit.WithSuppressed.
	auditRec = audit.SuppressingRecorder{Inner: auditRec}
	mcptools.SetAuditRecorder(auditRec)
	auth.SetAuditRecorder(auditRec)

	notarizer := buildAuditNotarizer(ctx, cfg)
	if notarizer != nil {
		notarizer.Start(ctx)
	}

	reader := buildAuditReader(ctx, cfg)
	clickHouse := buildAuditClickHouseReader(ctx, cfg)
	var querier audit.RowQuerier
	if reader != nil {
		querier = audit.NewQueryRouter(reader, clickHouse, cfg.AuditQueryRecentWindow)
	}
	return auditRuntime{
		Reader:     reader,
		ClickHouse: clickHouse,
		Querier:    querier,
		Redactor:   buildAuditRedactor(ctx, cfg),
		Notarizer:  notarizer,
		Recorder:   auditRec,
	}
}

func (r auditRuntime) Close() {
	if r.Reader != nil {
		r.Reader.Close()
	}
	if r.ClickHouse != nil {
		r.ClickHouse.Close()
	}
	if r.Redactor != nil {
		r.Redactor.Close()
	}
	if r.Notarizer != nil {
		_ = r.Notarizer.Close()
	}
	switch c := r.Recorder.(type) {
	case interface{ Close() error }:
		_ = c.Close()
	case interface{ Close() }:
		c.Close()
	}
}

func buildAuthMiddleware(
	cfg *config.Config,
	tokenRepo *postgres.TokenRepo,
) func(http.Handler) http.Handler {
	if cfg.Env == "development" {
		slog.Warn("dev_auth.enabled")
		return auth.DevBearer
	}
	return auth.Bearer(tokenRepo)
}

func buildServeMux(
	mcpHandler http.Handler,
	authMiddleware func(http.Handler) http.Handler,
) *http.ServeMux {
	mux := http.NewServeMux()
	mcpWithAuth := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed: streamable-http stateless mode requires POST", http.StatusMethodNotAllowed)
			return
		}
		authMiddleware(mcpHandler).ServeHTTP(w, r)
	})
	mux.Handle("/mcp", mcpWithAuth)
	mux.Handle("/mcp/", mcpWithAuth)
	registerConnectHandlers(mux, authMiddleware)

	// expvar counters at /debug/vars. expvar.Handler returns the standard
	// JSON dump of every registered Var; tack's counters are registered via
	// internal/telemetry/metrics.go. Not gated on auth so a local watcher
	// can scrape it directly. Bind to localhost only when this matters.
	mux.Handle("/debug/vars", expvar.Handler())
	return mux
}

// buildSearcher creates a Meilisearch client and ensures the nodes index is
// configured with a generic filterable set. Falls back to a no-op Searcher on
// setup failure.
func buildSearcher(cfg *config.Config) domainsearch.Searcher {
	meiliClient := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	// Generic filterable attributes only. Per-property filterability is added
	// by callers at index/update time via PropertyDef.Indexed.
	err := meiliClient.EnsureIndex("nodes", []string{"org_id", "node_type"})
	if err != nil {
		slog.Error("meilisearch.setup_failed",
			slog.String("url", cfg.MeiliURL),
			slog.String("err", err.Error()),
		)
		return searchadapter.Noop{}
	}
	slog.Info("meilisearch.connected", slog.String("url", cfg.MeiliURL))
	return meiliClient
}

// buildAuditRecorder selects the audit Recorder by configuration. When
// AUDIT_KAFKA_BROKERS is set it returns the Kafka producer, so Record publishes
// to Kafka and the audit-consumer becomes the only writer of audit.events.
// Otherwise, when AUDIT_WRITER_DSN is set, it returns the synchronous Yugabyte
// recorder. With neither configured it returns a NoopRecorder. A setup failure
// degrades to noop rather than blocking the server: audit is layered defense,
// not the primary path. Production deploys MUST set one of the two.
func buildAuditRecorder(ctx context.Context, cfg *config.Config) audit.Recorder {
	brokers := audit.SplitBrokers(cfg.AuditKafkaBrokers)
	if len(brokers) > 0 {
		kafkaRec, err := audit.NewKafkaRecorder(audit.KafkaConfig{
			Brokers:        brokers,
			Topic:          cfg.AuditKafkaTopic,
			ClientID:       cfg.AuditKafkaClientID,
			ProduceTimeout: cfg.AuditKafkaProduceTimeout,
		})
		if err != nil {
			slog.ErrorContext(ctx, "audit.kafka_setup_failed", slog.String("err", err.Error()))
			return audit.NoopRecorder{}
		}
		slog.InfoContext(ctx, "audit.kafka_enabled",
			slog.Int("broker_count", len(brokers)),
			slog.String("topic", cfg.AuditKafkaTopic),
		)
		return kafkaRec
	}
	if cfg.AuditWriterDSN == "" {
		slog.WarnContext(ctx, "audit.writer_disabled",
			slog.String("reason", "AUDIT_KAFKA_BROKERS and AUDIT_WRITER_DSN unset; ledger writes are noop"),
		)
		return audit.NoopRecorder{}
	}
	yb, err := audit.NewYBRecorder(ctx, cfg.AuditWriterDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.writer_setup_failed", slog.String("err", err.Error()))
		return audit.NoopRecorder{}
	}
	slog.InfoContext(ctx, "audit.writer_connected")
	return yb
}

// buildAuditReader opens the audit_reader pool when AUDIT_READER_DSN is
// set. nil return means the audit query MCP tools are not registered.
func buildAuditReader(ctx context.Context, cfg *config.Config) *audit.Reader {
	if cfg.AuditReaderDSN == "" {
		slog.InfoContext(ctx, "audit.reader_disabled",
			slog.String("reason", "AUDIT_READER_DSN unset; audit query tools disabled"),
		)
		return nil
	}
	rd, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.reader_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.reader_connected")
	return rd
}

// buildAuditClickHouseReader opens the ClickHouse read connection when
// AUDIT_CLICKHOUSE_DSN is set. nil means tack_audit_query reads Yugabyte only.
func buildAuditClickHouseReader(ctx context.Context, cfg *config.Config) *audit.ClickHouseReader {
	if cfg.AuditClickHouseDSN == "" {
		slog.InfoContext(ctx, "audit.clickhouse_reader_disabled",
			slog.String("reason", "AUDIT_CLICKHOUSE_DSN unset; audit queries read Yugabyte only"),
		)
		return nil
	}
	rd, err := audit.NewClickHouseReader(ctx, cfg.AuditClickHouseDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.clickhouse_reader_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.clickhouse_reader_connected")
	return rd
}

// buildAuditRedactor opens the audit_redactor pool when AUDIT_REDACTOR_DSN
// is set. nil disables the GDPR redaction MCP tool.
func buildAuditRedactor(ctx context.Context, cfg *config.Config) *audit.Redactor {
	if cfg.AuditRedactorDSN == "" {
		slog.InfoContext(ctx, "audit.redactor_disabled",
			slog.String("reason", "AUDIT_REDACTOR_DSN unset; redaction tool disabled"),
		)
		return nil
	}
	rd, err := audit.NewRedactor(ctx, cfg.AuditRedactorDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.redactor_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.redactor_connected")
	return rd
}

// buildAuditNotarizer assembles the periodic Merkle-root notarizer when a
// signing key path is configured. Reuses the audit_writer DSN.
func buildAuditNotarizer(ctx context.Context, cfg *config.Config) *audit.Notarizer {
	if cfg.AuditSigningKeyPath == "" || cfg.AuditWriterDSN == "" {
		slog.InfoContext(ctx, "audit.notarizer_disabled",
			slog.String("reason", "AUDIT_SIGNING_KEY_PATH or AUDIT_WRITER_DSN unset"),
		)
		return nil
	}
	n, err := audit.NewNotarizer(ctx, cfg.AuditWriterDSN, audit.NotarizerConfig{
		SigningKeyPath: cfg.AuditSigningKeyPath,
		Period:         0,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.notarizer_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.InfoContext(ctx, "audit.notarizer_started", slog.String("key_path", cfg.AuditSigningKeyPath))
	return n
}
