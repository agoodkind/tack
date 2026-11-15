package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"
	"goodkind.io/tack/internal/config"
)

// Provisioning initializes a fresh environment and permits repeated runs
// (TACK-303). The host-networked tack-ops container uses the Docker socket to
// run commands in the fdb and app containers. It cannot resolve bridge DNS names.

// fdbAvailableMarker is the substring fdbcli prints for a configured, healthy
// database. Its absence means the cluster has never been configured.
const fdbAvailableMarker = "The database is available"

// These constants identify the observed states of the provision steps.
const (
	provisionFDBAlreadyConfigured = "already_configured"
	provisionFDBConfigured        = "configured"
	provisionSeedCompleted        = "seeded"
	provisionSeedDeferred         = "deferred"
)

// provisionFDB configures a fresh FoundationDB cluster exactly once and
// returns its observed state. It uses `fdbcli status minimal` to detect a
// configured cluster and leaves that cluster unchanged. Destructive
// initialization requires explicit approval.
//
// The command runs fdbcli in a one-shot container against this guest's
// cluster file. It does not require a local store container. Data guests run
// the store processes, and the cluster file specifies their addresses
// (TACK-408).
//
// TACK_OPS_FDB_REDUNDANCY_MODE specifies replication. Configuration fails if
// the live processes cannot satisfy that mode. Provisioning permits repeated
// runs after the required processes start.
func provisionFDB(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	allowFDBInit bool,
) (string, error) {
	mode := cfg.OpsFDBRedundancyMode
	if err := validateRedundancyMode(mode); err != nil {
		slog.ErrorContext(ctx, "provision.fdb.redundancy_rejected", slog.String("err", err.Error()))
		return "", fmt.Errorf("provision fdb redundancy: %w", err)
	}

	// fdbcli reports an unconfigured cluster with a nonzero exit code.
	// Only a failure to run the container counts as an error here.
	status, _, err := readStoreCLI(ctx, cli, cfg, "status minimal")
	if err != nil {
		slog.ErrorContext(ctx, "provision.fdb.status_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("provision fdb status: %w", err)
	}

	if strings.Contains(status, fdbAvailableMarker) {
		slog.InfoContext(ctx, "provision.fdb.already_configured")
		return provisionFDBAlreadyConfigured, nil
	}

	if !allowFDBInit {
		unconfigured := errors.New(
			"provision: fdb is not configured and --allow-fdb-init was not passed; pass it ONLY on a truly fresh environment, NEVER on prod",
		)
		slog.ErrorContext(
			ctx,
			"provision.fdb.unconfigured_no_optin",
			slog.String("err", unconfigured.Error()),
		)

		return "", unconfigured
	}

	slog.WarnContext(ctx, "provision.fdb.configuring_new", slog.String("redundancy", mode))
	out, err := runStoreCLIWith(ctx, cli, cfg, "configure new "+mode+" "+storeStorageEngine)
	if err != nil {
		slog.ErrorContext(ctx, "provision.fdb.configure_failed", slog.String("err", err.Error()))
		return "", fmt.Errorf("provision fdb configure: %w", err)
	}

	slog.InfoContext(ctx, "provision.fdb.configured", slog.String("out", out))

	return provisionFDBConfigured, nil
}

// provisionSeed runs `/server seed` inside the app container, which connects
// to FoundationDB on the bridge. The provision config supplies SEED_EMAIL and
// SEED_NAME. Seed refuses execution when an org already exists. This command
// substitutes for the planned typed FDB operations (TACK-318).
// It returns the observed seed state.
func provisionSeed(ctx context.Context, cli *client.Client, cfg *config.Config) (string, error) {
	slog.InfoContext(ctx, "provision.seed.start", slog.String("container", cfg.OpsAppContainer))
	env := []string{"SEED_EMAIL=" + cfg.SeedEmail, "SEED_NAME=" + cfg.SeedName}
	var out bytes.Buffer
	code, stderr, err := containerExecStreaming(
		ctx,
		cli,
		cfg.OpsAppContainer,
		[]string{"/server", "seed"},
		env,
		&out,
	)
	if err != nil {
		// On first boot, the app container may still await fdb health and
		// cannot accept exec. Defer seed until a later provisioning run
		// after the app starts. Repeated provisioning runs are safe.
		slog.WarnContext(
			ctx,
			"provision.seed.deferred",
			slog.String(
				"reason",
				"app container not execable yet; re-run provision after the app is running",
			),
			slog.String("err", err.Error()),
		)

		return provisionSeedDeferred, nil
	}

	if code != 0 {
		seedErr := fmt.Errorf("provision seed: app container exited %d", code)
		slog.ErrorContext(
			ctx,
			"provision.seed.nonzero_exit",
			slog.Int("code", code),
			slog.String("stderr", strings.TrimSpace(stderr)),
			slog.String("err", seedErr.Error()),
		)

		return "", seedErr
	}

	slog.InfoContext(ctx, "provision.seed.completed")

	return provisionSeedCompleted, nil
}
