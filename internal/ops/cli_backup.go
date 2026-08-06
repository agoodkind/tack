package ops

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
)

// backupCommand builds the `ops backup` subtree. The subcommands initialize
// object-store bucket and recovery schedules, export snapshots, and drill
// restores into throwaway containers.
func backupCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage production datastore backups",
		Long:  "Subcommands initialize an object-store bucket and recovery schedules, export snapshots, and drill restores.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return fmt.Errorf("ops backup requires a subcommand (%s); the bare command deliberately runs nothing", strings.Join(backupSubcommandNames(cmd), ", "))
		},
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "buckets-init",
			Short: "Idempotently create the SeaweedFS S3 backup bucket",
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

// backupSubcommandNames lists the registered subcommands so the refusal
// error always matches reality.
func backupSubcommandNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		names = append(names, strings.Fields(sub.Use)[0])
	}
	return names
}
