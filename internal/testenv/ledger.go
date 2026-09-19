package testenv

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the pgx database/sql driver goose migrates through
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pressly/goose/v3"

	"goodkind.io/tack/migrations"
)

const (
	// ledgerService is the stack file service whose image the ledger runs.
	ledgerService = "yugabyte"
	// ledgerPort is the engine's YSQL port.
	ledgerPort = "5433"
	// ledgerAdminUser is the engine superuser the container is started with.
	ledgerAdminUser = "yugabyte"
	// ledgerDatabase is the database the container creates at start and the
	// one the migrations run in. It answers only once the engine has applied
	// the password, so readiness is probed against it.
	ledgerDatabase = "tack"
	// ledgerProbeTimeout bounds one readiness probe.
	ledgerProbeTimeout = 10 * time.Second
)

// ledgerCommand starts a single-node engine. One tablet per table on the one
// tablet server, where the engine's default sizes by core count, keeps each
// migration's many CREATE TABLE statements, and the weekly partitions behind
// them, from creating tablets by the dozen; the test ledger carries a few
// rows per test, so tablet count changes nothing the tests observe.
var ledgerCommand = []string{
	"/home/yugabyte/bin/yugabyted", "start", "--daemon=false", "--base_dir=/home/yugabyte/var",
	"--tserver_flags=ysql_num_shards_per_tserver=1,yb_num_shards_per_tserver=1",
}

// ledgerPlatform pins the ledger to the amd64 build. The arm64 build of the
// pinned release corrupts itself under the chain-append concurrency test
// (TACK-459), so an arm64 host runs the amd64 build under emulation; on an
// amd64 host the pin changes nothing.
func ledgerPlatform() *ocispec.Platform {
	return &ocispec.Platform{Architecture: "amd64", OS: "linux", OSVersion: "", OSFeatures: nil, Variant: ""}
}

// provisionLedger starts this process's ledger engine, migrates it, and
// returns its connection string. The superuser credential is generated here
// and lives only in the container's environment and the returned DSN.
func provisionLedger(ctx context.Context) (string, error) {
	image, err := serviceImage(ctx, ledgerService)
	if err != nil {
		return "", err
	}
	superuserKey, err := randomHex(ctx, 16)
	if err != nil {
		return "", err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	started, err := startEngine(ctx, cli, engineSpec{
		kind:     "yugabyte",
		image:    image,
		platform: ledgerPlatform(),
		cmd:      ledgerCommand,
		env: []string{
			"YSQL_USER=" + ledgerAdminUser,
			"YSQL_PASSWORD=" + superuserKey,
			"YSQL_DB=" + ledgerDatabase,
		},
	})
	if err != nil {
		return "", err
	}
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(ledgerAdminUser, superuserKey),
		Host:     net.JoinHostPort(started.address, ledgerPort),
		Path:     "/" + ledgerDatabase,
		RawQuery: "sslmode=disable",
	}
	if err := waitForLedger(ctx, dsn.String()); err != nil {
		return "", err
	}
	if err := migrateLedger(ctx, dsn.String()); err != nil {
		return "", err
	}
	slog.InfoContext(ctx, "testenv.ledger.ready", slog.String("container", started.name))
	return dsn.String(), nil
}

// waitForLedger polls until the engine answers a query as the superuser, or
// fails when ctx ends with the last probe's error.
func waitForLedger(ctx context.Context, dsn string) error {
	for {
		lastErr := probeLedger(ctx, dsn)
		if lastErr == nil {
			return nil
		}
		if !sleepOrDone(ctx) {
			slog.ErrorContext(ctx, "testenv.ledger.not_ready", slog.String("err", lastErr.Error()))
			return fmt.Errorf("the test ledger did not answer before the deadline: %w", lastErr)
		}
	}
}

// probeLedger makes one bounded connection and query against the engine. A
// failed probe is expected while the engine starts, so it logs at debug and
// returns the reason as text for the final deadline error to carry.
func probeLedger(ctx context.Context, dsn string) error {
	probeCtx, cancel := context.WithTimeout(ctx, ledgerProbeTimeout)
	defer cancel()
	connection, err := pgx.Connect(probeCtx, dsn)
	if err != nil {
		slog.DebugContext(ctx, "testenv.ledger.probe", slog.String("err", err.Error()))
		return errors.New("connect: " + err.Error())
	}
	defer func() { _ = connection.Close(context.WithoutCancel(ctx)) }()
	var answer int
	if err := connection.QueryRow(probeCtx, "SELECT 1").Scan(&answer); err != nil {
		slog.DebugContext(ctx, "testenv.ledger.probe", slog.String("err", err.Error()))
		return errors.New("query: " + err.Error())
	}
	return nil
}

// migrateLedger applies every embedded migration, the same set
// `./server migrate` applies.
func migrateLedger(ctx context.Context, dsn string) error {
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.open_failed", slog.String("err", err.Error()))
		return fmt.Errorf("open the test ledger: %w", err)
	}
	defer func() { _ = database.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, database, migrations.FS)
	if err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.migrate_failed", slog.String("err", err.Error()))
		return fmt.Errorf("load migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		slog.ErrorContext(ctx, "testenv.ledger.migrate_failed", slog.String("err", err.Error()))
		return fmt.Errorf("migrate the test ledger: %w", err)
	}
	return nil
}
