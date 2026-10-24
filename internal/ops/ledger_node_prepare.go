// ledger_node_prepare.go runs `ops ledger node-prepare` on a ledger guest: it
// reads the node's saved launcher config out of its container, compares the
// saved master list with the environment's, and when they differ rewrites the
// file and stops the container. The deploy's next `docker compose up` then
// starts the node against the right masters.

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

// ledgerNodePrepareLabel is the command label in the result and in errors.
const ledgerNodePrepareLabel = "ops ledger node-prepare"

// ledgerNodePrepareResult is what the command emits: the saved master list
// before and after, the flags that differ between the running server and the
// flag file, and whether the command stopped the node for the restart.
type ledgerNodePrepareResult struct {
	clispec.ResultMarker
	Command    string   `json:"command"`
	Container  string   `json:"container"`
	State      string   `json:"state"`
	Before     string   `json:"before,omitempty"`
	After      string   `json:"after,omitempty"`
	StaleFlags []string `json:"stale_flags,omitempty"`
	Stopped    bool     `json:"stopped"`
	DryRun     bool     `json:"dry_run"`
}

func (ledgerNodePrepareResult) isStepEvidence() {}

// ledgerNodePreparePlan is the observed state of the local node and the change
// that the command makes with --execute.
type ledgerNodePreparePlan struct {
	result  ledgerNodePrepareResult
	saved   launcherConfigFile
	wanted  []string
	rewrite bool
}

// planLedgerNodePrepare reads the local node's saved master list and stale
// flags. It returns the result that a dry run reports.
func planLedgerNodePrepare(ctx context.Context, cfg *config.Config, cli *client.Client) (ledgerNodePreparePlan, error) {
	var unread launcherConfigFile
	plan := ledgerNodePreparePlan{
		result: ledgerNodePrepareResult{
			ResultMarker: clispec.ResultMarker{}, Command: ledgerNodePrepareLabel, Container: yugabyteBackupContainer,
			State: "", Before: "", After: "", StaleFlags: nil, Stopped: false, DryRun: true,
		},
		saved: unread, wanted: nil, rewrite: false,
	}
	names, err := deployedLedgerNodeNames(cfg)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.failed", slog.String("err", err.Error()))
		return plan, fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
	}
	plan.wanted = ledgerMasterList(names)
	saved, found, err := readLauncherConfig(ctx, cli, yugabyteBackupContainer)
	if err != nil {
		return plan, fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
	}
	if !found {
		plan.result.State = ledgerNodeStateAbsent
		slog.InfoContext(ctx, "ops.ledger.node_prepare.absent", slog.String("container", yugabyteBackupContainer))
		return plan, nil
	}
	plan.saved = saved
	current, err := savedLedgerMasters(ctx, saved.content)
	if err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.failed", slog.String("err", err.Error()))
		return plan, fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
	}
	plan.result.Before, plan.result.After = strings.Join(current, ","), strings.Join(plan.wanted, ",")
	plan.result.State, plan.rewrite = ledgerNodeStateChanged, !equalStringSets(current, plan.wanted)
	if plan.rewrite {
		return plan, nil
	}
	stale, err := staleLedgerNodeFlags(ctx, cli, yugabyteBackupContainer)
	if err != nil {
		return plan, fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
	}
	plan.result.StaleFlags = stale
	if len(stale) == 0 {
		plan.result.State = ledgerNodeStateUnchanged
		slog.InfoContext(ctx, "ops.ledger.node_prepare.unchanged", slog.String("masters", plan.result.Before))
	}
	return plan, nil
}

// applyLedgerNodePrepare rewrites the saved master list when it differs and
// stops the container when the plan changed the node. It returns the result
// that the command reports after --execute.
func applyLedgerNodePrepare(ctx context.Context, cli *client.Client, plan ledgerNodePreparePlan) (ledgerNodePrepareResult, error) {
	result := plan.result
	result.DryRun = false
	if result.State != ledgerNodeStateChanged {
		return result, nil
	}
	if plan.rewrite {
		rewritten, err := rewriteLedgerMasters(ctx, plan.saved.content, plan.wanted)
		if err != nil {
			slog.ErrorContext(ctx, "ops.ledger.node_prepare.failed", slog.String("err", err.Error()))
			return result, fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
		}
		if err := writeLauncherConfig(ctx, cli, yugabyteBackupContainer, plan.saved.header, rewritten); err != nil {
			return result, fmt.Errorf("%s: %w", ledgerNodePrepareLabel, err)
		}
	}
	if _, err := cli.ContainerStop(ctx, yugabyteBackupContainer, client.ContainerStopOptions{}); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.stop_failed", slog.String("err", err.Error()))
		return result, fmt.Errorf("%s: stop %s after the change: %w", ledgerNodePrepareLabel, yugabyteBackupContainer, err)
	}
	result.Stopped = true
	slog.InfoContext(ctx, "ops.ledger.node_prepare.changed", slog.String("before", result.Before),
		slog.String("after", result.After), slog.Any("stale_flags", result.StaleFlags))
	return result, nil
}

// writeLedgerNodePrepareResult emits the result through the sink.
func writeLedgerNodePrepareResult(ctx context.Context, sink clispec.ResultSink, result ledgerNodePrepareResult) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "ops.ledger.node_prepare.write_result_failed", slog.String("err", err.Error()))
		return fmt.Errorf("%s: write result: %w", result.Command, err)
	}
	return nil
}

// equalStringSets reports whether two sorted, deduplicated lists contain the
// same entries.
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
