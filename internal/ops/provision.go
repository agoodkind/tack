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

// provision is the idempotent first-boot entrypoint (TACK-303). One ordered
// action brings a fresh environment to ready and is a safe no-op on an
// already-provisioned prod. It runs from the host-networked tack-ops container
// and reaches the bridge-resident fdb and app containers through the Docker
// socket, because tack-ops cannot resolve the bridge DNS names directly.

// fdbAvailableMarker is the substring fdbcli prints for a configured, healthy
// database. Its absence means the cluster has never been configured.
const fdbAvailableMarker = "The database is available"

// Observed states of the provision steps.
const (
	provisionFDBAlreadyConfigured = "already_configured"
	provisionFDBConfigured        = "configured"
	provisionSeedCompleted        = "seeded"
	provisionSeedDeferred         = "deferred"
)

// provisionFDB configures a fresh FoundationDB cluster exactly once. A
// configured cluster is detected via `fdbcli status minimal` and left
// untouched. Configuring is destructive, so it requires the explicit opt-in.
//
// It reads and configures through a one-shot fdbcli container against this
// guest's cluster file, not by exec-ing into a local store container: the
// guest that provisions runs no store process once the store is on the data
// guests, and the cluster file names those guests' addresses (TACK-408).
//
// The redundancy comes from TACK_OPS_FDB_REDUNDANCY_MODE, so a rebuilt
// environment comes back with the replication it had. On a fresh environment
// where the store processes are not all up yet, the configure fails here
// rather than setting a mode the live processes cannot satisfy; provision is
// re-runnable.
//
// It returns the observed state of the cluster.
func provisionFDB(ctx context.Context, cli *client.Client, cfg *config.Config, allowFDBInit bool) (string, error) {
	mode := cfg.OpsFDBRedundancyMode
	if err := validateRedundancyMode(mode); err != nil {
		slog.ErrorContext(ctx, "provision.fdb.redundancy_rejected", slog.String("err", err.Error()))
		return "", fmt.Errorf("provision fdb redundancy: %w", err)
	}
	// fdbcli exits nonzero when the cluster it is asked about was never
	// configured, which is the reading this step wants. The exit code is
	// therefore data; a container that could not run at all is the error.
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
		unconfigured := errors.New("provision: fdb is not configured and --allow-fdb-init was not passed; pass it ONLY on a truly fresh environment, NEVER on prod")
		slog.ErrorContext(ctx, "provision.fdb.unconfigured_no_optin", slog.String("err", unconfigured.Error()))
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

// provisionSeed runs `/server seed` inside the app container, which is on the
// bridge and reaches FoundationDB. SEED_EMAIL and SEED_NAME are injected from
// the provision config; seed is idempotent (it no-ops when an org already
// exists). This is the stopgap until the typed FDB-ops path lands (TACK-318).
//
// It returns the observed state of the seed.
func provisionSeed(ctx context.Context, cli *client.Client, cfg *config.Config) (string, error) {
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
		return provisionSeedDeferred, nil
	}
	if code != 0 {
		seedErr := fmt.Errorf("provision seed: app container exited %d", code)
		slog.ErrorContext(ctx, "provision.seed.nonzero_exit",
			slog.Int("code", code),
			slog.String("stderr", strings.TrimSpace(stderr)),
			slog.String("err", seedErr.Error()))
		return "", seedErr
	}
	slog.InfoContext(ctx, "provision.seed.completed")
	return provisionSeedCompleted, nil
}
