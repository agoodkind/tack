package ops

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// ledgerNodeTarget is the external target reference of the local ledger node.
const ledgerNodeTarget = "docker/" + yugabyteBackupContainer

// ledgerNodeWaitInput contains the two windows of the wait, as durations in
// Go syntax (5m, 10s).
type ledgerNodeWaitInput struct {
	Stall string `json:"stall"`
	Poll  string `json:"poll"`
}

func (ledgerNodeWaitInput) isCommandInput() {}

// ledgerNodePrepareCommand builds `ops ledger node-prepare`. With --execute,
// the command records the observed node state and any change as one external
// step. Without --execute, it records the read of the node state and prints
// the planned change.
func ledgerNodePrepareCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node-prepare",
		Short: "Align this guest's ledger node saved master list with the environment before it starts",
		Long: "Reads the node's saved launcher config out of its container, compares " +
			"its master list with the names in TACK_LEDGER_NODE_HOSTS, and when they " +
			"differ rewrites the list and stops the container. The next compose up " +
			"starts the node against the right masters. Prints changed, unchanged, or " +
			"absent (no container or no saved config). Without --execute it reports " +
			"and writes nothing.",
		Args: cobra.NoArgs,
	}
	spec := externalSpec(audit.VerbOpsLedgerNodePrepare, ledgerNodeTarget)
	clispec.AttachExternalPreview(cmd, f, spec, emptyCommandInput,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runLedgerNodePrepareRecorded(ctx, f.Cfg, clispec.NewCLISink(f), steps)
		},
		func(ctx context.Context) error {
			return previewLedgerNodePrepare(ctx, f, clispec.NewCLISink(f))
		})
	return cmd
}

// ledgerNodeWaitCommand builds `ops ledger node-wait`. With --execute, the
// command records the wait as one external step with the observed health.
func ledgerNodeWaitCommand(f *cli.Factory) *cobra.Command {
	in := ledgerNodeWaitInput{Stall: ledgerNodeWaitDefaultStall.String(), Poll: ledgerNodeWaitDefaultPoll.String()}
	cmd := &cobra.Command{
		Use:   "node-wait",
		Short: "Wait until this guest's ledger node is healthy, bounded by inactivity rather than a clock",
		Long: "Polls the node's container health and the master quorum's dead node and " +
			"under-replicated tablet counts. A count that falls is progress; the wait " +
			"fails only when nothing has moved for the stall window or the container " +
			"stops. The wait does not bound the total time, and a node replaying a large log " +
			"waits for as long as it needs.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(&in.Stall, "stall", ledgerNodeWaitDefaultStall.String(), "how long with no progress before the wait fails")
	cmd.Flags().StringVar(&in.Poll, "poll", ledgerNodeWaitDefaultPoll.String(), "how often to read the node and the masters")
	input := func() (json.RawMessage, error) { return encodeCommandInput(in) }
	clispec.AttachExternal(cmd, f, externalSpec(audit.VerbOpsLedgerNodeWait, ledgerNodeTarget+"/health"), input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runLedgerNodeWaitRecorded(ctx, f.Cfg, in, clispec.NewCLISink(f), steps)
		})
	return cmd
}
