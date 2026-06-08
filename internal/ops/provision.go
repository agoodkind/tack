package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/migrations"
)

// provision is the idempotent first-boot entrypoint (TACK-303). One ordered
// action brings a fresh environment to ready and is a safe no-op on an
// already-provisioned prod. It runs from the host-networked tack-ops container
// and reaches the bridge-resident fdb and app containers through the Docker
// socket, because tack-ops cannot resolve the bridge DNS names directly.

// fdbAvailableMarker is the substring fdbcli prints for a configured, healthy
// database. Its absence means the cluster has never been configured.
const fdbAvailableMarker = "The database is available"

type provisionInput struct {
	clispec.InputMarker `exhaustruct:"optional"`
	AllowFDBInit        bool
}

// provisionOp declares `ops provision`. The destructive fdb step is gated behind
// --allow-fdb-init (default false) so it can never run against an
// already-configured production cluster by accident.
func provisionOp(f *cli.Factory) clispec.Operation[provisionInput] {
	return clispec.Operation[provisionInput]{
		Name:     clispec.Name{Canonical: "provision", CLIOverride: ""},
		Group:    opsGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Bring a fresh environment to ready: fdb init (opt-in), migrate, seed audit roles, seed product",
		Long:     "Ordered and idempotent: configure fdb only when unconfigured and --allow-fdb-init is set (fresh environments only, never prod), run migrations, seed the audit roles, then seed initial product data when SEED_EMAIL and SEED_NAME are set. Safe to re-run; each step no-ops when already done.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[provisionInput]{
			clispec.BoolParam("allow-fdb-init",
				"Permit 'fdbcli configure new' when fdb is unconfigured. FRESH ENVIRONMENTS ONLY; never pass on prod.",
				false, func(in *provisionInput, v bool) { in.AllowFDBInit = v }),
		},
		New: func() provisionInput { return provisionInput{InputMarker: clispec.InputMarker{}, AllowFDBInit: false} },
		Run: func(ctx context.Context, in provisionInput, sink clispec.ResultSink) error {
			if err := provisionRun(ctx, f.Cfg, in.AllowFDBInit); err != nil {
				return err
			}
			return sink.WriteText(ctx, "provision complete")
		},
	}
}

// provisionRun executes the ordered first-boot steps. Each is idempotent so the
// whole command converges a partially-provisioned environment.
func provisionRun(ctx context.Context, cfg *config.Config, allowFDBInit bool) error {
	cli, err := newDockerClient(ctx)
	if err != nil {
		return fmt.Errorf("provision docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	if err := provisionFDB(ctx, cli, cfg.OpsFDBContainer, allowFDBInit); err != nil {
		return err
	}

	slog.InfoContext(ctx, "provision.migrate.start")
	if err := postgres.Migrate(ctx, cfg.DatabaseURL, migrations.FS); err != nil {
		slog.ErrorContext(ctx, "provision.migrate.failed", slog.String("err", err.Error()))
		return fmt.Errorf("provision migrate: %w", err)
	}

	slog.InfoContext(ctx, "provision.seed_roles.start")
	if err := RunAuditSeedRoles(ctx, cfg); err != nil {
		return fmt.Errorf("provision seed roles: %w", err)
	}

	if cfg.SeedEmail == "" || cfg.SeedName == "" {
		slog.InfoContext(ctx, "provision.seed.skipped",
			slog.String("reason", "SEED_EMAIL and SEED_NAME unset; product seed is left to a deliberate run"))
		return nil
	}
	return provisionSeed(ctx, cli, cfg)
}

// provisionFDB configures a fresh FoundationDB cluster exactly once. A
// configured cluster is detected via `fdbcli status minimal` and left
// untouched. Configuring is destructive, so it requires the explicit opt-in.
func provisionFDB(ctx context.Context, cli *client.Client, containerName string, allowFDBInit bool) error {
	status, err := containerExec(ctx, cli, containerName, []string{"fdbcli", "--exec", "status minimal"})
	if err != nil {
		slog.ErrorContext(ctx, "provision.fdb.status_failed", slog.String("err", err.Error()))
		return fmt.Errorf("provision fdb status: %w", err)
	}
	if strings.Contains(status.Stdout, fdbAvailableMarker) {
		slog.InfoContext(ctx, "provision.fdb.already_configured")
		return nil
	}
	if !allowFDBInit {
		unconfigured := errors.New("provision: fdb is not configured and --allow-fdb-init was not passed; pass it ONLY on a truly fresh environment, NEVER on prod")
		slog.ErrorContext(ctx, "provision.fdb.unconfigured_no_optin", slog.String("err", unconfigured.Error()))
		return unconfigured
	}
	slog.WarnContext(ctx, "provision.fdb.configuring_new", slog.String("container", containerName))
	res, err := containerExec(ctx, cli, containerName, []string{"fdbcli", "--exec", "configure new single ssd"})
	if err != nil {
		slog.ErrorContext(ctx, "provision.fdb.configure_failed", slog.String("err", err.Error()))
		return fmt.Errorf("provision fdb configure: %w", err)
	}
	slog.InfoContext(ctx, "provision.fdb.configured", slog.String("out", strings.TrimSpace(res.Stdout)))
	return nil
}

// provisionSeed runs `/server seed` inside the app container, which is on the
// bridge and reaches FoundationDB. SEED_EMAIL and SEED_NAME are injected from
// the provision config; seed is idempotent (it no-ops when an org already
// exists). This is the stopgap until the typed FDB-ops path lands (TACK-318).
func provisionSeed(ctx context.Context, cli *client.Client, cfg *config.Config) error {
	slog.InfoContext(ctx, "provision.seed.start", slog.String("container", cfg.OpsAppContainer))
	env := []string{"SEED_EMAIL=" + cfg.SeedEmail, "SEED_NAME=" + cfg.SeedName}
	var out bytes.Buffer
	code, stderr, err := containerExecStreaming(ctx, cli, cfg.OpsAppContainer, []string{"/server", "seed"}, env, &out)
	if err != nil {
		// On a truly fresh boot the app container is created but not yet running
		// (it waits for fdb health, which this same run just configured), so the
		// exec cannot attach. Defer rather than fail: provision is idempotent, so
		// re-running it once the app is up completes the seed.
		slog.WarnContext(ctx, "provision.seed.deferred",
			slog.String("reason", "app container not execable yet; re-run provision after the app is running"),
			slog.String("err", err.Error()))
		return nil
	}
	if code != 0 {
		seedErr := fmt.Errorf("provision seed: app container exited %d", code)
		slog.ErrorContext(ctx, "provision.seed.nonzero_exit",
			slog.Int("code", code),
			slog.String("stderr", strings.TrimSpace(stderr)),
			slog.String("err", seedErr.Error()))
		return seedErr
	}
	slog.InfoContext(ctx, "provision.seed.completed")
	return nil
}
