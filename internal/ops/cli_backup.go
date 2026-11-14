package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// backupCommand builds commands for bucket and schedule initialization,
// snapshot export, and restore drills in disposable containers.
func backupCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Manage production datastore backups",
		Long:  "Subcommands initialize an object-store bucket and recovery schedules, export snapshots, and drill restores.",
	}
	clispec.AttachRead(
		cmd,
		f,
		singletonReadSpec(audit.VerbOpsBackup, "backup"),
		emptyCommandInput,
		func(context.Context) (audit.OperatorReadOutput, error) {
			return audit.OperatorReadOutput{}, fmt.Errorf(
				"ops backup requires a subcommand (%s); the bare command does not run operations",
				strings.Join(backupSubcommandNames(cmd), ", "),
			)
		},
	)
	cmd.AddCommand(
		backupLeaf(
			f,
			"buckets-init",
			"Idempotently create the SeaweedFS S3 backup bucket",
			audit.VerbOpsBackupBucketsInit,
			runBackupBucketsInitRecorded,
		),
		backupLeaf(
			f,
			"yb-pitr-init",
			"Create the YugabyteDB point-in-time-recovery snapshot schedule",
			audit.VerbOpsBackupYBPITRInit,
			runBackupYBPITRInitRecorded,
		),
		backupLeaf(
			f,
			"yb-snapshot-export",
			"Export a YugabyteDB distributed snapshot off-host to the object store",
			audit.VerbOpsBackupYBSnapshotExport,
			runBackupYBSnapshotExportRecorded,
		),
		backupYBArchiveNodeCommand(f),
		backupRestoreDrillCommand(f),
		backupLeaf(
			f,
			"fdb-continuous-init",
			"Start the FoundationDB continuous backup session (idempotent)",
			audit.VerbOpsBackupFDBContinuousInit,
			runBackupFDBContinuousInitRecorded,
		),
		backupStalenessCheckCommand(f),
		backupFDBRestoreVersionCommand(f),
		backupRetentionCommand(f),
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
	run func(context.Context, *config.Config, audit.StepRecorder) (audit.OutcomeDetails, error),
) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.NoArgs}
	clispec.AttachExternal(
		cmd,
		f,
		backupSpec(verb, use),
		emptyCommandInput,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return run(ctx, f.Cfg, steps)
		},
	)

	return cmd
}

// backupYBArchiveNodeCommand archives this node's files for a snapshot export.
// --run-key selects an export run. Without it, the command selects the newest
// manifest with an incomplete prefix for this node. The manifest supplies the
// snapshot ID to prevent archiving files for a different snapshot.
func backupYBArchiveNodeCommand(f *cli.Factory) *cobra.Command {
	var runKey string
	cmd := &cobra.Command{
		Use:   "yb-archive-node",
		Short: "Archive this node's YugabyteDB tablet snapshot files for an export run",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(
		&runKey,
		"run-key",
		"",
		"export run key to archive (default: the newest manifest in the object store)",
	)
	input := func() (json.RawMessage, error) {
		return encodeCommandInput(archiveNodeInput{RunKey: runKey})
	}
	spec := backupSpec(audit.VerbOpsBackupYBArchiveNode, "yb-archive-node")
	clispec.AttachExternal(
		cmd,
		f,
		spec,
		input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			return runBackupYBArchiveNodeRecorded(ctx, f.Cfg, runKey, steps)
		},
	)

	return cmd
}

// backupRestoreDrillCommand configures restore targets. --yb-run-key selects
// an export run and refuses incomplete runs. Without it, the drill selects the
// newest complete run; the latest export may still await node archives.
//
// --fdb-target-time selects an explicit moment. The drill refuses moments
// outside the backup window or with fractions fdbrestore cannot represent.
// Without the flag, the FoundationDB restore uses the latest restorable point.
func backupRestoreDrillCommand(f *cli.Factory) *cobra.Command {
	var ybRunKey string
	var fdbTargetTime string
	cmd := &cobra.Command{
		Use:   "restore-drill",
		Short: "Restore each store into throwaway containers and assert data is present",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(
		&ybRunKey,
		"yb-run-key",
		"",
		"yugabyte export run key to drill, refused if incomplete (default: the newest complete run)",
	)
	cmd.Flags().StringVar(
		&fdbTargetTime,
		"fdb-target-time",
		"",
		"whole second to restore FoundationDB to, as RFC 3339 (2026-08-30T01:07:23Z) or 2026/08/30.01:07:23+0000,"+
			" refused if outside the backup's restorable window (default: the latest restorable point)",
	)
	input := func() (json.RawMessage, error) {
		return encodeCommandInput(restoreDrillInput{
			YBRunKey:      ybRunKey,
			FDBTargetTime: fdbTargetTime,
		})
	}
	spec := backupSpec(audit.VerbOpsBackupRestoreDrill, "restore-drill")
	clispec.AttachExternal(
		cmd,
		f,
		spec,
		input,
		func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
			opts := RestoreDrillOptions{YBRunKey: ybRunKey, FDBTargetTime: nil}
			if fdbTargetTime != "" {
				parsed, err := parseFDBTargetTime(fdbTargetTime)
				if err != nil {
					return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
				}

				opts.FDBTargetTime = &parsed
			}

			return runBackupRestoreDrillRecorded(ctx, f.Cfg, opts, steps)
		},
	)

	return cmd
}

// backupStalenessCheckCommand records the external check outcome before
// returning a stale verdict error.
func backupStalenessCheckCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "staleness-check",
		Short: "Report how long ago each backup mechanism last succeeded, exiting nonzero past threshold",
		Args:  cobra.NoArgs,
	}
	spec := backupSpec(audit.VerbOpsBackupStalenessCheck, "staleness-check")
	clispec.AttachExternalCheck(
		cmd,
		f,
		spec,
		emptyCommandInput,
		func(ctx context.Context, steps audit.StepRecorder) (clispec.CheckOutcome, error) {
			details, stale, err := runBackupStalenessCheckRecorded(
				ctx,
				f.Cfg,
				cmd.OutOrStdout(),
				steps,
			)

			return clispec.CheckOutcome{Details: details, Verdict: stale}, err
		},
	)

	return cmd
}

// backupFDBRestoreVersionCommand converts a time to an FDB restore version
// using only object storage. It does not require the source cluster, which may
// be unavailable after a total loss (TACK-468).
func backupFDBRestoreVersionCommand(f *cli.Factory) *cobra.Command {
	var at string
	cmd := &cobra.Command{
		Use:   "restore-version",
		Short: "Convert a moment into the FoundationDB version to restore to, from the object store alone",
		Args:  cobra.NoArgs,
	}
	cmd.Flags().StringVar(
		&at,
		"at",
		"",
		"the moment to restore to, as RFC 3339 (2026-08-30T01:07:23Z) or 2026/08/30.01:07:23+0000 (default: now)",
	)
	clispec.AttachAudit(
		cmd,
		f,
		audit.Spec{Verb: string(audit.VerbOpsBackupFDBRestoreVersion), Reads: true},
		func(ctx context.Context) error {
			want := opsNow().UTC()
			if at != "" {
				parsed, err := parseFDBTargetTime(at)
				if err != nil {
					return err
				}

				want = parsed
			}

			return RunBackupFDBRestoreVersion(ctx, f.Cfg, want, cmd.OutOrStdout())
		},
	)

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

// backupSubcommandNames lists registered subcommands for the refusal error.
func backupSubcommandNames(cmd *cobra.Command) []string {
	names := make([]string, 0, len(cmd.Commands()))
	for _, sub := range cmd.Commands() {
		names = append(names, strings.Fields(sub.Use)[0])
	}

	return names
}
