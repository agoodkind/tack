// ledger_bootstrap_wait_admin.go runs the yb-admin reads of `ops ledger
// bootstrap-wait` in one-shot containers against the first nodes of the
// ledger node list.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// ybAdminAtMasters runs one yb-admin subcommand in a one-shot container with
// masters.names as the master list and masters.extraHosts as the
// container's hosts entries. The image, the network, and the ledger TLS
// flags and mounts come from cfg through ybAdminClusterAccess.
func ybAdminAtMasters(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	masters ledgerBootstrapMasters,
	subcommand string,
) (execResult, error) {
	logger := telemetry.L(ctx)
	scoped := *cfg
	scoped.BackupYBMasterAddresses = masters.names
	accessArgs, accessBinds := ybAdminClusterAccess(&scoped)
	res, err := runOneShot(ctx, cli, logger, runOneShotOptions{
		Image:      cfg.BackupYBImage,
		Network:    cfg.BackupFDBNetwork,
		Entrypoint: []string{ybAdminBinary},
		Cmd:        append(accessArgs, subcommand),
		Env:        nil,
		Binds:      accessBinds,
		ExtraHosts: masters.extraHosts,
		Name:       "",
	})
	if err != nil {
		wrapped := fmt.Errorf("yb-admin %s: %w", subcommand, err)
		slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.yb_admin_failed",
			slog.String("subcommand", subcommand), slog.String("err", wrapped.Error()))
		return res, wrapped
	}
	if res.ExitCode != 0 {
		wrapped := fmt.Errorf("yb-admin %s exited %d: %s", subcommand, res.ExitCode,
			strings.TrimSpace(res.Stdout+" "+res.Stderr))
		slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.yb_admin_failed",
			slog.String("subcommand", subcommand), slog.String("err", wrapped.Error()))
		return res, wrapped
	}
	return res, nil
}
