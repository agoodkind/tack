package ops

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

func (BackupRetentionOptions) isCommandInput() {}

func backupRetentionCommand(factory *cli.Factory) *cobra.Command {
	options := BackupRetentionOptions{KeepDays: defaultBackupRetentionDays}
	command := &cobra.Command{
		Use: "retention", Short: "Preview or expire old backup data while preserving the retained restore window", Args: cobra.NoArgs,
	}
	command.Flags().IntVar(&options.KeepDays, "keep-days", defaultBackupRetentionDays, "number of days of restorable backups to retain")
	input := func() (json.RawMessage, error) {
		if _, err := retentionCutoff(options); err != nil {
			return nil, err
		}
		return encodeCommandInput(options)
	}
	clispec.AttachExternalPreview(command, factory, backupSpec(audit.VerbOpsBackupRetention, "retention"), input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return RunBackupRetention(ctx, factory.Cfg, options, command.OutOrStdout(), steps)
		},
		func(ctx context.Context) error {
			return PreviewBackupRetention(ctx, factory.Cfg, options, command.OutOrStdout())
		})
	return command
}
