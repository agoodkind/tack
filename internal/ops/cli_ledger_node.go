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

// ledgerNodeWaitInput specifies stall and poll durations in Go syntax, such as
// 5m and 10s.
type ledgerNodeWaitInput struct {
	Stall string `json:"stall"`
	Poll  string `json:"poll"`
}

func (ledgerNodeWaitInput) isCommandInput() {}

// ledgerNodePrepareCommand records observed state and changes as an external
// step with --execute. Without it, the command records a read and prints the
// plan.
func ledgerNodePrepareCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "node-prepare",
		Short: "Align this guest's saved ledger master list before startup",
		Long: "Read the node's saved launcher config using the container API and " +
			"compare its master list with TACK_LEDGER_NODE_HOSTS. If they differ, " +
			"rewrite the list and stop the container. The next compose up starts " +
			"the node with the configured masters. The result is changed, unchanged, " +
			"or absent if the container or config does not exist. Without --execute, " +
			"the command reports planned changes without writing them.",
		Args: cobra.NoArgs,
	}
	spec := externalSpec(audit.VerbOpsLedgerNodePrepare, ledgerNodeTarget)
	clispec.AttachExternalPreview(
		cmd,
		f,
		spec,
		emptyCommandInput,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runLedgerNodePrepareRecorded(ctx, f.Cfg, clispec.NewCLISink(f), steps)
		},
		func(ctx context.Context) error {
			return previewLedgerNodePrepare(ctx, f, clispec.NewCLISink(f))
		},
	)

	return cmd
}

// ledgerNodeWaitCommand records the wait and observed health as one external
// step when the operator supplies --execute.
func ledgerNodeWaitCommand(f *cli.Factory) *cobra.Command {
	in := ledgerNodeWaitInput{
		Stall: ledgerNodeWaitDefaultStall.String(),
		Poll:  ledgerNodeWaitDefaultPoll.String(),
	}
	cmd := &cobra.Command{
		Use:   "node-wait",
		Short: "Wait for ledger node health with a limit on inactivity",
		Long: "Poll container health and the master quorum's dead-node and " +
			"under-replicated tablet counts. A decreasing count indicates progress. " +
			"The wait fails if counts do not improve during the stall window or " +
			"the container stops. The wait does not limit total recovery time; " +
			"a node can continue replaying a large log while it makes progress.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().StringVar(
		&in.Stall,
		"stall",
		ledgerNodeWaitDefaultStall.String(),
		"maximum duration without progress before the wait fails",
	)
	cmd.Flags().StringVar(
		&in.Poll,
		"poll",
		ledgerNodeWaitDefaultPoll.String(),
		"how often to read the node and the masters",
	)
	input := func() (json.RawMessage, error) { return encodeCommandInput(in) }
	clispec.AttachExternal(
		cmd,
		f,
		externalSpec(audit.VerbOpsLedgerNodeWait, ledgerNodeTarget+"/health"),
		input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runLedgerNodeWaitRecorded(ctx, f.Cfg, in, clispec.NewCLISink(f), steps)
		},
	)

	return cmd
}
