// ledger_bootstrap_wait_run.go runs `ops ledger bootstrap-wait`: it reads the
// cluster through yb-admin one-shots and the master health check on every
// poll, and returns at the first reading that matches the target or fails at
// the deadline with every count.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

const (
	// ledgerBootstrapCommandLabel prefixes this command's errors.
	ledgerBootstrapCommandLabel = "ops ledger bootstrap-wait"
	// ledgerBootstrapReadTimeout bounds one poll's reads. The deadline is
	// checked after each poll, and a deadline shorter than one poll fails with
	// the counts of that poll.
	ledgerBootstrapReadTimeout = time.Minute
)

// runLedgerBootstrapWait waits for target against the first target.masters
// ledger nodes and prints the elapsed time and every count.
func runLedgerBootstrapWait(
	ctx context.Context,
	cfg *config.Config,
	sink clispec.ResultSink,
	target ledgerBootstrapTarget,
	deadline, poll time.Duration,
) error {
	masters, err := ledgerBootstrapMasterList(cfg, target.masters)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", ledgerBootstrapCommandLabel, err)
	}
	cli, err := newDockerClient(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", ledgerBootstrapCommandLabel, err)
	}
	defer func() { _ = cli.Close() }()
	read := func(ctx context.Context) ledgerBootstrapReading {
		return readLedgerBootstrapCluster(ctx, cli, cfg, masters, target)
	}
	reading, elapsed, err := awaitLedgerBootstrap(ctx, read, target, deadline, poll)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.failed",
			slog.String("masters", masters.names), slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", ledgerBootstrapCommandLabel, err)
	}
	line := fmt.Sprintf("ledger bootstrap ready after %s against %s: %s",
		elapsed.Round(time.Millisecond), masters.names, reading.summary(target))
	slog.InfoContext(ctx, "ops.ledger.bootstrap_wait.ready",
		slog.Duration("elapsed", elapsed), slog.String("counts", reading.summary(target)))
	if err := sink.WriteText(ctx, line); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.write_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write result: %w", ledgerBootstrapCommandLabel, err)
	}
	return nil
}

// awaitLedgerBootstrap polls read until a reading satisfies target or the
// deadline passes. A failed read is one more poll, not a failure.
func awaitLedgerBootstrap(
	ctx context.Context,
	read func(context.Context) ledgerBootstrapReading,
	target ledgerBootstrapTarget,
	deadline, poll time.Duration,
) (ledgerBootstrapReading, time.Duration, error) {
	started := opsNow()
	for {
		reading := read(ctx)
		elapsed := opsNow().Sub(started)
		if reading.satisfies(target) {
			return reading, elapsed, nil
		}
		remaining := deadline - elapsed
		if remaining <= 0 {
			expired := fmt.Errorf("the ledger did not report %s within the %s deadline: elapsed %s: %s",
				targetText(target), deadline, elapsed.Round(time.Millisecond), reading.summary(target))
			slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.deadline_passed", slog.String("err", expired.Error()))
			return reading, elapsed, expired
		}
		if !opsWait(ctx, min(poll, remaining)) {
			cancelled := fmt.Errorf("waiting for %s, cancelled after %s: %s: %w",
				targetText(target), elapsed.Round(time.Millisecond), reading.summary(target), ctx.Err())
			slog.ErrorContext(ctx, "ops.ledger.bootstrap_wait.cancelled", slog.String("err", cancelled.Error()))
			return reading, elapsed, cancelled
		}
	}
}

// targetText renders the target in the same keys as a reading's summary.
func targetText(target ledgerBootstrapTarget) string {
	text := fmt.Sprintf("masters=%d tablet_servers=%d", target.masters, target.tabletServers)
	if target.replicas > 0 {
		text += fmt.Sprintf(" num_replicas=%d under_replicated_tablets=0", target.replicas)
	}
	return text
}

// readLedgerBootstrapCluster takes one reading. A non-zero yb-admin exit or a
// failed health check leaves that count unread and adds the error to the
// reading's last error.
func readLedgerBootstrapCluster(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	masters ledgerBootstrapMasters,
	target ledgerBootstrapTarget,
) ledgerBootstrapReading {
	readCtx, cancel := context.WithTimeout(ctx, ledgerBootstrapReadTimeout)
	defer cancel()
	var reading ledgerBootstrapReading
	var failures []string
	masterList, err := ybAdminAtMasters(readCtx, cli, cfg, masters, "list_all_masters")
	if err != nil {
		failures = append(failures, err.Error())
	} else {
		reading.masters, reading.mastersRead = countAliveRows(masterList.Stdout), true
	}
	tabletServers, err := ybAdminAtMasters(readCtx, cli, cfg, masters, "list_all_tablet_servers")
	if err != nil {
		failures = append(failures, err.Error())
	} else {
		reading.tabletServers, reading.tabletServersRead = countAliveRows(tabletServers.Stdout), true
	}
	if target.replicas > 0 {
		failures = append(failures, readLedgerReplication(readCtx, cli, cfg, masters, &reading)...)
	}
	reading.lastError = strings.Join(failures, "; ")
	return reading
}

// readLedgerReplication reads numReplicas and the under-replicated tablet
// count into reading and returns the errors of the reads that failed.
func readLedgerReplication(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	masters ledgerBootstrapMasters,
	reading *ledgerBootstrapReading,
) []string {
	var failures []string
	universe, err := ybAdminAtMasters(ctx, cli, cfg, masters, "get_universe_config")
	if err == nil {
		reading.replicas, err = unmarshalUniverseNumReplicas(ctx, universe.Stdout)
	}
	if err != nil {
		failures = append(failures, err.Error())
	} else {
		reading.replicasRead = true
	}
	counts, err := probeLedgerClusterCounts(ctx, masters.health)
	if err != nil {
		failures = append(failures, err.Error())
	} else {
		reading.underReplicated, reading.underReplicatedRead = counts.underReplicated, true
	}
	return failures
}
