package ops

import (
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
)

// backupCommand builds the `ops backup` subtree. The bare command runs a full
// snapshot; the subcommands initialize the object-store buckets and recovery
// schedules, verify an existing backup directory, and drill restores into
// throwaway containers.
func backupCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot and verify the production datastores",
		Long:  "Run a full snapshot of FDB, Yugabyte, Temporal-DB, and Meilisearch. Subcommands initialize the object-store buckets and recovery schedules, verify an existing backup directory, and drill restores.",
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
			Use:   "buckets-init",
			Short: "Idempotently create the SeaweedFS S3 backup buckets",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return RunBackupBucketsInit(cmd.Context(), f.Cfg)
			},
		},
		&cobra.Command{
			Use:   "yb-pitr-init",
			Short: "Create the YugabyteDB point-in-time-recovery snapshot schedule",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return RunBackupYBPITRInit(cmd.Context(), f.Cfg)
			},
		},
		&cobra.Command{
			Use:   "yb-snapshot-export",
			Short: "Export a YugabyteDB distributed snapshot off-host to the object store",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return RunBackupYBSnapshotExport(cmd.Context(), f.Cfg)
			},
		},
		&cobra.Command{
			Use:   "restore-drill",
			Short: "Restore each store into throwaway containers and assert data is present",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return RunBackupRestoreDrill(cmd.Context(), f.Cfg)
			},
		},
		&cobra.Command{
			Use:   "fdb-continuous-init",
			Short: "Start the FoundationDB continuous backup session (idempotent)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return RunBackupFDBContinuousInit(cmd.Context(), f.Cfg)
			},
		},
	)
	return cmd
}
