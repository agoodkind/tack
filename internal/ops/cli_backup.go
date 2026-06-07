package ops

import (
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
)

// backupCommand builds the `ops backup` subtree. The bare command runs a full
// snapshot; the subcommands operate on an existing backup directory.
func backupCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot, verify, and restore-test the production datastores",
		Long:  "Run a full snapshot of FDB, Yugabyte, Temporal-DB, and Meilisearch. Subcommands operate on an existing backup directory.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBackupRun(cmd.Context(), f.Cfg)
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "verify <backup-dir>",
			Short: "Structural inventory check of an existing backup directory",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return RunBackupVerify(cmd.Context(), args[0])
			},
		},
		&cobra.Command{
			Use:   "restore-test <backup-dir>",
			Short: "End-to-end restore replay against scratch containers",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return RunBackupRestoreTest(cmd.Context(), f.Cfg, args[0])
			},
		},
	)
	return cmd
}
