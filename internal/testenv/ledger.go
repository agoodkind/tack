package testenv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	// ledgerService is the stack file service whose image the ledger runs.
	ledgerService = "yugabyte"
	// ledgerPort is the engine's YSQL port.
	ledgerPort = "5433"
	// ledgerAdminUser is the engine superuser the container is started with.
	ledgerAdminUser = "yugabyte"
	// ledgerSetupDatabase is the database the container creates at start. It
	// answers only once the engine has applied the password, so readiness is
	// probed against it, and test databases are created from it.
	ledgerSetupDatabase = "tack"
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

// provisionLedger starts or reuses the ledger engine and returns the
// connection string of a new migrated database for this process.
func provisionLedger(ctx context.Context) (string, error) {
	image, err := serviceImage(ctx, ledgerService)
	if err != nil {
		return "", err
	}
	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = cli.Close() }()
	running, err := ensureEngine(ctx, cli, engineSpec{
		name:     engineName("yugabyte", image, ledgerCommand),
		image:    image,
		platform: ledgerPlatform(),
		cmd:      ledgerCommand,
		env: func(credential string) []string {
			return []string{
				"YSQL_USER=" + ledgerAdminUser,
				"YSQL_PASSWORD=" + credential,
				"YSQL_DB=" + ledgerSetupDatabase,
			}
		},
	})
	if err != nil {
		return "", err
	}
	setupDSN := ledgerDSN(running, ledgerSetupDatabase)
	if err := waitForLedger(ctx, setupDSN); err != nil {
		return "", err
	}
	return createLedgerDatabase(ctx, running, setupDSN)
}

// ledgerDSN is the superuser connection string for one database on the
// engine. The credential comes from the running container, never from source.
func ledgerDSN(running engine, database string) string {
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(ledgerAdminUser, running.superuserKey),
		Host:     net.JoinHostPort(running.address, ledgerPort),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return dsn.String()
}

// waitForLedger polls until the engine answers a query as the superuser, or
// fails at the readiness deadline with the last probe's error.
func waitForLedger(ctx context.Context, dsn string) error {
	ctx, cancel := context.WithTimeout(ctx, readyTimeout)
	defer cancel()
	var lastErr error
	for {
		lastErr = probeLedger(ctx, dsn)
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
