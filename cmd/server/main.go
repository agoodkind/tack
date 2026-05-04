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
	"goodkind.io/tack/internal/repair"
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
		case "repair":
			repair.Run(cfg, os.Args[2:])
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
				os.Exit(1)
			}
			if err := audit.GenerateAuditSigningKey(os.Args[2]); err != nil {
				slog.Error("gen-audit-key", "err", err)
				os.Exit(1)
			}
			slog.Info("gen-audit-key", "path", os.Args[2])
			return
		}
	}

	runServer(cfg)
}

func runMigrations(cfg *config.Config) {
	ctx := context.Background()
	if err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}
	slog.Info("migrations complete")
}

func runServer(cfg *config.Config) {
	ctx := context.Background()

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.Error("postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	fdbStores, err := fdbadapter.NewStores(cfg.FDBClusterFile, pool)
	if err != nil {
		slog.Error("foundationdb", "err", err)
		os.Exit(1)
	}

	searcher := buildSearcher(cfg)

	auditRec := buildAuditRecorder(ctx, cfg)
	// Wrap once at startup so seed and other internal automation can mute
	// emission via audit.WithSuppressed.
	auditRec = audit.SuppressingRecorder{Inner: auditRec}
	mcptools.SetAuditRecorder(auditRec)
	auth.SetAuditRecorder(auditRec)

	auditReader := buildAuditReader(ctx, cfg)
	defer func() {
		if auditReader != nil {
			auditReader.Close()
		}
	}()
	auditRedactor := buildAuditRedactor(ctx, cfg)
	defer func() {
		if auditRedactor != nil {
			auditRedactor.Close()
		}
	}()
	notarizer := buildAuditNotarizer(ctx, cfg)
	if notarizer != nil {
		notarizer.Start(ctx)
		defer func() { _ = notarizer.Close() }()
	}
	defer func() {
		switch c := auditRec.(type) {
		case interface{ Close() error }:
			_ = c.Close()
		case interface{ Close() }:
			c.Close()
		}
	}()
	_ = auditRec // wired into NodeService + MCP wrapper in TACK-174

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
		AuditReader:   auditReader,
		AuditRedactor: auditRedactor,
	})

	var authMiddleware func(http.Handler) http.Handler
	if cfg.Env == "development" {
		slog.Warn("running in dev auth mode. Bearer token is treated as a raw user UUID")
		authMiddleware = auth.DevBearer
	} else {
		authMiddleware = auth.Bearer(tokenRepo)
	}

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
		Addr:    addr,
		Handler: h2c.NewHandler(telemetry.RequestLogger(mux), &http2.Server{}),
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down")
	shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}

// buildSearcher creates a Meilisearch client and ensures the nodes index is
// configured with a generic filterable set. Falls back to a no-op Searcher on
// setup failure.
func buildSearcher(cfg *config.Config) domainsearch.Searcher {
	meiliClient := searchadapter.New(cfg.MeiliURL, cfg.MeiliMasterKey)
	// Generic filterable attributes only. Per-property filterability is added
	// by callers at index/update time via PropertyDef.Indexed.
	if err := meiliClient.EnsureIndex("nodes", []string{"org_id", "node_type"}); err != nil {
		slog.Error("meilisearch.setup_failed",
			slog.String("url", cfg.MeiliURL),
			slog.String("err", err.Error()),
		)
		return searchadapter.Noop{}
	}
	slog.Info("meilisearch.connected", slog.String("url", cfg.MeiliURL))
	return meiliClient
}

// buildAuditRecorder opens the audit_writer pool when AUDIT_WRITER_DSN is
// set; otherwise returns a NoopRecorder so callers can always call Record.
// A failure to open the pool is logged loudly and degrades to noop rather
// than blocking the server: audit is layered defense, not the primary path.
// Production deploys MUST set AUDIT_WRITER_DSN; the noop path is for dev.
//
// When AUDIT_WAL_DIR resolves to a non-empty path, the YBRecorder is wrapped
// in a WALRecorder so read-class verbs persist to a local fsync'd segment
// log first and the drainer ships them to Yugabyte asynchronously.
func buildAuditRecorder(ctx context.Context, cfg *config.Config) audit.Recorder {
	if cfg.AuditWriterDSN == "" {
		slog.Warn("audit.writer_disabled",
			slog.String("reason", "AUDIT_WRITER_DSN unset; ledger writes are noop"),
		)
		return audit.NoopRecorder{}
	}
	yb, err := audit.NewYBRecorder(ctx, cfg.AuditWriterDSN)
	if err != nil {
		slog.Error("audit.writer_setup_failed", slog.String("err", err.Error()))
		return audit.NoopRecorder{}
	}
	slog.Info("audit.writer_connected")
	if cfg.AuditWALDir == "" {
		return yb
	}
	wal, err := audit.NewWALRecorder(ctx, yb, audit.WALConfig{Dir: cfg.AuditWALDir})
	if err != nil {
		slog.Error("audit.wal_setup_failed",
			slog.String("dir", cfg.AuditWALDir),
			slog.String("err", err.Error()),
		)
		return yb
	}
	slog.Info("audit.wal_enabled", slog.String("dir", cfg.AuditWALDir))
	return wal
}

// buildAuditReader opens the audit_reader pool when AUDIT_READER_DSN is
// set. nil return means the audit query MCP tools are not registered.
func buildAuditReader(ctx context.Context, cfg *config.Config) *audit.Reader {
	if cfg.AuditReaderDSN == "" {
		slog.Info("audit.reader_disabled",
			slog.String("reason", "AUDIT_READER_DSN unset; audit query tools disabled"),
		)
		return nil
	}
	rd, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
	if err != nil {
		slog.Error("audit.reader_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.Info("audit.reader_connected")
	return rd
}

// buildAuditRedactor opens the audit_redactor pool when AUDIT_REDACTOR_DSN
// is set. nil disables the GDPR redaction MCP tool.
func buildAuditRedactor(ctx context.Context, cfg *config.Config) *audit.Redactor {
	if cfg.AuditRedactorDSN == "" {
		slog.Info("audit.redactor_disabled",
			slog.String("reason", "AUDIT_REDACTOR_DSN unset; redaction tool disabled"),
		)
		return nil
	}
	rd, err := audit.NewRedactor(ctx, cfg.AuditRedactorDSN)
	if err != nil {
		slog.Error("audit.redactor_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.Info("audit.redactor_connected")
	return rd
}

// buildAuditNotarizer assembles the periodic Merkle-root notarizer when a
// signing key path is configured. Reuses the audit_writer DSN.
func buildAuditNotarizer(ctx context.Context, cfg *config.Config) *audit.Notarizer {
	if cfg.AuditSigningKeyPath == "" || cfg.AuditWriterDSN == "" {
		slog.Info("audit.notarizer_disabled",
			slog.String("reason", "AUDIT_SIGNING_KEY_PATH or AUDIT_WRITER_DSN unset"),
		)
		return nil
	}
	n, err := audit.NewNotarizer(ctx, cfg.AuditWriterDSN, audit.NotarizerConfig{
		SigningKeyPath: cfg.AuditSigningKeyPath,
	})
	if err != nil {
		slog.Error("audit.notarizer_setup_failed", slog.String("err", err.Error()))
		return nil
	}
	slog.Info("audit.notarizer_started", slog.String("key_path", cfg.AuditSigningKeyPath))
	return n
}
