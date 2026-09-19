package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// ledgerGroup holds the commands a deploy runs around a ledger node's restart
// on the guest that serves it. Both act on this guest's own node only.
var ledgerGroup = &clispec.Group{
	Use: "ledger", Short: "Prepare and wait on this guest's ledger node around a restart",
	Long: "", Parent: opsGroup,
}

// ledgerNodeWaitInput carries the two windows the wait runs under, as
// durations in Go syntax (5m, 10s).
type ledgerNodeWaitInput struct {
	clispec.InputMarker
	Stall string
	Poll  string
}

// ledgerNodePrepareOp declares `ops ledger node-prepare`.
func ledgerNodePrepareOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:     clispec.Name{Canonical: "node-prepare", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsLedgerNodePrepare), Mutates: true},
		Group:    ledgerGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Align this guest's ledger node saved master list with the environment before it starts",
		Long: "Reads the node's saved launcher config out of its container, compares " +
			"its master list with the names in TACK_LEDGER_NODE_HOSTS, and when they " +
			"differ rewrites the list and stops the container so the next compose up " +
			"starts the node against the right masters. Prints changed, unchanged, or " +
			"absent (no container or no saved config). Without --execute it reports " +
			"and writes nothing.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		DryRun: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runLedgerNodePrepare(ctx, f.Cfg, sink, false)
		},
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			return runLedgerNodePrepare(ctx, f.Cfg, sink, true)
		},
	}
}

// ledgerNodeWaitOp declares `ops ledger node-wait`.
func ledgerNodeWaitOp(f *cli.Factory) clispec.Operation[ledgerNodeWaitInput] {
	return clispec.Operation[ledgerNodeWaitInput]{
		Name:     clispec.Name{Canonical: "node-wait", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsLedgerNodeWait), Reads: true},
		Group:    ledgerGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Wait until this guest's ledger node is healthy, bounded by inactivity rather than a clock",
		Long: "Polls the node's container health and the master quorum's dead node and " +
			"under-replicated tablet counts. A count that falls is progress; the wait " +
			"fails only when nothing has moved for the stall window or the container " +
			"stops. There is no bound on total time, so a node replaying a large log " +
			"waits for as long as it needs.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[ledgerNodeWaitInput]{
			clispec.StringParam("stall", "how long with no progress before the wait fails",
				ledgerNodeWaitDefaultStall.String(), false,
				func(in *ledgerNodeWaitInput, v string) { in.Stall = v }),
			clispec.StringParam("poll", "how often to read the node and the masters",
				ledgerNodeWaitDefaultPoll.String(), false,
				func(in *ledgerNodeWaitInput, v string) { in.Poll = v }),
		},
		New: func() ledgerNodeWaitInput {
			return ledgerNodeWaitInput{
				InputMarker: clispec.InputMarker{},
				Stall:       ledgerNodeWaitDefaultStall.String(),
				Poll:        ledgerNodeWaitDefaultPoll.String(),
			}
		},
		Run: func(ctx context.Context, in ledgerNodeWaitInput, sink clispec.ResultSink) error {
			stall, poll, err := ledgerNodeWaitWindows(in)
			if err != nil {
				slog.ErrorContext(ctx, "ops.ledger.node_wait.failed", slog.String("err", err.Error()))
				return fmt.Errorf("ops ledger node-wait: %w", err)
			}
			return runLedgerNodeWait(ctx, f.Cfg, sink, stall, poll)
		},
	}
}

// ledgerNodeWaitWindows parses the two flags and refuses a window that
// could never end the wait or never let it read.
func ledgerNodeWaitWindows(in ledgerNodeWaitInput) (stall, poll time.Duration, err error) {
	stall, err = time.ParseDuration(in.Stall)
	if err != nil || stall <= 0 {
		return 0, 0, fmt.Errorf("--stall %q is not a positive duration", in.Stall)
	}
	poll, err = time.ParseDuration(in.Poll)
	if err != nil || poll <= 0 {
		return 0, 0, fmt.Errorf("--poll %q is not a positive duration", in.Poll)
	}
	return stall, poll, nil
}
