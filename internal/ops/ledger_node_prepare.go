// ledger_node_prepare.go runs `ops ledger node-prepare` on a ledger guest: it
// reads the node's saved launcher config out of its container, compares the
// saved master list with the environment's, and when they differ rewrites the
// file and stops the container so the deploy's next `docker compose up` starts
// the node against the right masters.

package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// Outcomes the command reports. The deploy reads only the exit code; the
// state is for the operator and the ledger.
const (
	ledgerNodeStateChanged   = "changed"
	ledgerNodeStateUnchanged = "unchanged"
	ledgerNodeStateAbsent    = "absent"
)

// ledgerNodePrepareResult is what the command emits: what the saved list was,
// what it is now, and whether the node was stopped for the restart.
type ledgerNodePrepareResult struct {
	clispec.ResultMarker
	Command   string `json:"command"`
	Container string `json:"container"`
	State     string `json:"state"`
	Before    string `json:"before,omitempty"`
	After     string `json:"after,omitempty"`
	Stopped   bool   `json:"stopped"`
	DryRun    bool   `json:"dry_run"`
}

// runLedgerNodePrepare aligns the local node's saved master list. With
// execute false it reports what it would change and writes nothing.
func runLedgerNodePrepare(ctx context.Context, cfg *config.Config, sink clispec.ResultSink, execute bool) error {
	const command = "ops ledger node-prepare"
	names, err := deployedLedgerNodeNames(cfg)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", command, err)
	}
	wanted := ledgerMasterList(names)
	cli, err := newDockerClient(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	defer func() { _ = cli.Close() }()

	result := ledgerNodePrepareResult{
		ResultMarker: clispec.ResultMarker{}, Command: command, Container: yugabyteBackupContainer,
		State: "", Before: "", After: "", Stopped: false, DryRun: !execute,
	}
	saved, found, err := readLauncherConfig(ctx, cli, yugabyteBackupContainer)
	if err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	if !found {
		result.State = ledgerNodeStateAbsent
		slog.InfoContext(ctx, "ops.ledger.node_prepare.absent", slog.String("container", yugabyteBackupContainer))
		return writeLedgerNodePrepareResult(ctx, sink, result)
	}
	current, err := savedLedgerMasters(ctx, saved.content)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", command, err)
	}
	result.Before = strings.Join(current, ",")
	result.After = strings.Join(wanted, ",")
	if equalStringSets(current, wanted) {
		result.State = ledgerNodeStateUnchanged
		slog.InfoContext(ctx, "ops.ledger.node_prepare.unchanged", slog.String("masters", result.Before))
		return writeLedgerNodePrepareResult(ctx, sink, result)
	}
	result.State = ledgerNodeStateChanged
	if !execute {
		return writeLedgerNodePrepareResult(ctx, sink, result)
	}
	rewritten, err := rewriteLedgerMasters(ctx, saved.content, wanted)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: %w", command, err)
	}
	if err := writeLauncherConfig(ctx, cli, yugabyteBackupContainer, saved.header, rewritten); err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	if _, err := cli.ContainerStop(ctx, yugabyteBackupContainer, client.ContainerStopOptions{}); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.stop_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: stop %s after rewriting its master list: %w", command, yugabyteBackupContainer, err)
	}
	result.Stopped = true
	slog.InfoContext(ctx, "ops.ledger.node_prepare.changed",
		slog.String("before", result.Before), slog.String("after", result.After))
	return writeLedgerNodePrepareResult(ctx, sink, result)
}

// writeLedgerNodePrepareResult emits the result through the sink.
func writeLedgerNodePrepareResult(ctx context.Context, sink clispec.ResultSink, result ledgerNodePrepareResult) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.write_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write result: %w", result.Command, err)
	}
	return nil
}

// equalStringSets reports whether two sorted, deduplicated lists hold the same
// entries.
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
