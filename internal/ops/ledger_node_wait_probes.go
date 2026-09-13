// ledger_node_wait_probes.go wires `ops ledger node-wait` to the two things it
// reads: the local ledger container's health from the Docker daemon and the
// cluster's counts from whichever configured master answers.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// ledgerNodeHealthy is the health status the container reports once its
// probe passes, as the Docker API spells it.
const ledgerNodeHealthy = string(container.Healthy)

// runLedgerNodeWait waits for the local ledger node under the given windows
// and reports how long it took.
func runLedgerNodeWait(ctx context.Context, cfg *config.Config, sink clispec.ResultSink, stallWindow, pollInterval time.Duration) error {
	const command = "ops ledger node-wait"
	cli, err := newDockerClient(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	defer func() { _ = cli.Close() }()
	watch := ledgerNodeWatch{
		Health: func(ctx context.Context) (ledgerNodeHealth, error) {
			return inspectLedgerNodeHealth(ctx, cli, yugabyteBackupContainer)
		},
		Cluster: func(ctx context.Context) (ledgerClusterCounts, error) {
			return probeLedgerClusterCounts(ctx, cfg.BackupYBMasterAddresses)
		},
	}
	took, err := awaitLedgerNode(ctx, watch, stallWindow, pollInterval)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_wait.failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", command, err)
	}
	slog.InfoContext(ctx, "ops.ledger.node_wait.healthy", slog.Duration("took", took))
	line := fmt.Sprintf("%s: healthy after %s", yugabyteBackupContainer, took.Round(time.Second))
	if err := sink.WriteText(ctx, line); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_wait.write_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write result: %w", command, err)
	}
	return nil
}

// inspectLedgerNodeHealth reads the container's running state and health
// status. A container without a health check reports its status as none,
// which the wait never mistakes for healthy.
func inspectLedgerNodeHealth(ctx context.Context, cli *client.Client, containerName string) (ledgerNodeHealth, error) {
	inspected, err := cli.ContainerInspect(ctx, containerName, client.ContainerInspectOptions{Size: false})
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_wait.inspect_failed",
			slog.String("container", containerName), slog.String("err", err.Error()))
		return ledgerNodeHealth{}, fmt.Errorf("inspect %s: %w", containerName, err)
	}
	state := inspected.Container.State
	if state == nil {
		return ledgerNodeHealth{}, fmt.Errorf("inspect %s: the daemon reported no state", containerName)
	}
	status := string(container.NoHealthcheck)
	if state.Health != nil {
		status = string(state.Health.Status)
	}
	return ledgerNodeHealth{status: status, running: state.Running}, nil
}

// probeLedgerClusterCounts asks each configured master in turn for the cluster
// health summary and returns the first usable answer.
func probeLedgerClusterCounts(ctx context.Context, masterAddresses string) (ledgerClusterCounts, error) {
	urls := ybMasterHealthURLs(masterAddresses)
	if len(urls) == 0 {
		return ledgerClusterCounts{}, errors.New("TACK_BACKUP_YB_MASTER_ADDRESSES names no master to read")
	}
	var failures []string
	for _, url := range urls {
		body, err := fetchYBMasterHealth(ctx, url)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		counts, err := ledgerClusterCountsFromBody(ctx, body)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		return counts, nil
	}
	return ledgerClusterCounts{}, errors.New("no master answered the health check: " + strings.Join(failures, "; "))
}

// ledgerClusterCountsFromBody reads the two counts out of a master's
// health-check payload. A payload missing either list is an error, so an
// endpoint that answers 200 with something else never vouches for progress.
func ledgerClusterCountsFromBody(ctx context.Context, body []byte) (ledgerClusterCounts, error) {
	var payload ybMasterHealthCheck
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.WarnContext(ctx, "ops.ledger.node_wait.health_unparseable", slog.String("err", err.Error()))
		return ledgerClusterCounts{}, fmt.Errorf("unmarshal master health check: %w", err)
	}
	if payload.DeadNodes == nil || payload.UnderReplicatedTablets == nil {
		return ledgerClusterCounts{}, errors.New("master health check omits dead_nodes or under_replicated_tablets")
	}
	return ledgerClusterCounts{
		deadNodes:       len(*payload.DeadNodes),
		underReplicated: len(*payload.UnderReplicatedTablets),
	}, nil
}
