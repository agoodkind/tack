package testenv

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
)

const (
	// postgresqlImage is the PostgreSQL release that provider tests run
	// against, pinned by tag and by the digest of its multi-platform image
	// index. The stack file does not list it because production runs YugabyteDB.
	postgresqlImage = "postgres:17.11-alpine@sha256:266d3c257594e195b309ae9d1805e54c788f1d7b375f56bf6c367aac6c917e7b"
	// postgresqlPort is the server's TCP port.
	postgresqlPort = "5432"
	// postgresqlAdminUser is the superuser the container is started with.
	postgresqlAdminUser = "postgres"
	// postgresqlDatabase is the database the container creates at start.
	postgresqlDatabase = "postgres"
	// postgresqlPasswordBytes is the random size of the generated superuser
	// password.
	postgresqlPasswordBytes = 16
)

var postgresqlState provisioned

// PostgreSQL returns the connection string of this process's PostgreSQL server,
// which has no Tack schema. The login is the server superuser, which permits a
// test to create roles and databases.
func PostgreSQL(t T) string {
	t.Helper()
	skipWhenShort(t)

	return postgresqlState.get(t, provisionPostgreSQL)
}

// provisionPostgreSQL starts this process's PostgreSQL server and returns its
// connection string. The superuser password is generated here and exists only
// in the container's environment and the returned connection string.
func provisionPostgreSQL(ctx context.Context) (string, error) {
	superuserKey, err := randomHex(ctx, postgresqlPasswordBytes)
	if err != nil {
		return "", err
	}

	cli, err := dockerClient(ctx)
	if err != nil {
		return "", err
	}

	defer func() { _ = cli.Close() }()
	started, err := startEngine(
		ctx,
		cli,
		engineSpec{
			kind:     "postgresql",
			image:    postgresqlImage,
			platform: nil,
			cmd:      nil,
			env:      []string{"POSTGRES_PASSWORD=" + superuserKey},
		},
	)
	if err != nil {
		return "", err
	}

	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(postgresqlAdminUser, superuserKey),
		Host:     net.JoinHostPort(started.address, postgresqlPort),
		Path:     "/" + postgresqlDatabase,
		RawQuery: "sslmode=disable",
	}
	if err := waitForPostgreSQL(ctx, dsn.String()); err != nil {
		return "", err
	}

	slog.InfoContext(ctx, "testenv.postgresql.ready", slog.String("container", started.name))

	return dsn.String(), nil
}

// waitForPostgreSQL polls until the server answers a query as the superuser,
// or fails when ctx ends with the last probe's error.
func waitForPostgreSQL(ctx context.Context, dsn string) error {
	for {
		lastErr := probeLedger(ctx, dsn)
		if lastErr == nil {
			return nil
		}

		if !sleepOrDone(ctx) {
			slog.ErrorContext(
				ctx,
				"testenv.postgresql.not_ready",
				slog.String("err", lastErr.Error()),
			)

			return fmt.Errorf(
				"the test PostgreSQL server did not answer before the deadline: %w",
				lastErr,
			)
		}
	}
}
