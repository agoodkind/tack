package ops

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// backupCommand builds the `ops backup` subtree. The subcommands initialize
// object-store bucket and recovery schedules, export snapshots, and drill
// restores into throwaway containers.
func backupCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage production datastore backups",
		Long:  "Subcommands initialize an object-store bucket and recovery schedules, export snapshots, and drill restores.",
	}
	clispec.AttachAudit(cmd, f, audit.Spec{Verb: string(audit.VerbOpsBackup), Reads: true}, func(ctx context.Context) error {
		return fmt.Errorf("ops backup requires a subcommand (%s); the bare command deliberately runs nothing", strings.Join(backupSubcommandNames(cmd), ", "))
	})
	cmd.AddCommand(
		backupLeaf(f, "buckets-init", "Idempotently create the SeaweedFS S3 backup bucket", audit.VerbOpsBackupBucketsInit, RunBackupBucketsInit),
		backupLeaf(f, "yb-pitr-init", "Create the YugabyteDB point-in-time-recovery snapshot schedule", audit.VerbOpsBackupYBPITRInit, RunBackupYBPITRInit),
		backupLeaf(f, "yb-snapshot-export", "Export a YugabyteDB distributed snapshot off-host to the object store", audit.VerbOpsBackupYBSnapshotExport, RunBackupYBSnapshotExport),
		backupYBArchiveNodeCommand(f),
		backupLeaf(f, "restore-drill", "Restore each store into throwaway containers and assert data is present", audit.VerbOpsBackupRestoreDrill, RunBackupRestoreDrill),
		backupLeaf(f, "fdb-continuous-init", "Start the FoundationDB continuous backup session (idempotent)", audit.VerbOpsBackupFDBContinuousInit, RunBackupFDBContinuousInit),
	)
	cmd.SetHelpCommand(backupHelpCommand())
	cmd.InitDefaultHelpCmd()
	return cmd
}

func backupLeaf(
	f *cli.Factory,
	use string,
	short string,
	verb audit.Verb,
	run func(context.Context, *config.Config) error,
) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs}
	clispec.AttachAudit(cmd, f, audit.Spec{Verb: string(verb), Mutates: true}, func(ctx context.Context) error {
		return run(ctx, f.Cfg)
	})
	return cmd
}

// backupYBArchiveNodeCommand builds the per-node half of the yb snapshot
// export. It is the one backup leaf with a flag: --run-key targets a specific
// export run, and without it the command discovers the newest manifest whose
// prefix for this node is not yet filled. The snapshot id always comes from
// the run's manifest so the archived files and the manifest can never name
// different snapshots.
func backupYBArchiveNodeCommand(f *cli.Factory) *cobra.Command {
	var runKey string
	cmd := &cobra.Command{
		Use:   "yb-archive-node",
		Short: "Archive this node's YugabyteDB tablet snapshot files for an export run",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(&runKey, "run-key", "",
		"export run key to archive (default: the newest manifest in the object store)")
	clispec.AttachAudit(cmd, f, audit.Spec{Verb: string(audit.VerbOpsBackupYBArchiveNode), Mutates: true}, func(ctx context.Context) error {
		return RunBackupYBArchiveNode(ctx, f.Cfg, runKey)
	})
	return cmd
}

func backupHelpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command]",
		Short: "Help about any command",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parent := cmd.Parent()
			if len(args) == 0 {
				return parent.Help()
			}
			for _, child := range parent.Commands() {
				if child.Name() == args[0] {
					return child.Help()
				}
			}
			return fmt.Errorf("unknown backup help topic %q", args[0])
		},
	}
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
