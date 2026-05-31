package ops

import (
	"context"
	"fmt"
	"os"

	"goodkind.io/tack/internal/config"
)

// backupSubcommand names the CLI subverbs under the backup family. Using
// a typed enum (rather than bare string literals) keeps the dispatcher
// exhaustive-checkable as the family grows.
type backupSubcommand string

const (
	backupSubHelp         backupSubcommand = "help"
	backupSubVerify       backupSubcommand = "verify"
	backupSubBucketsInit  backupSubcommand = "buckets-init"
	backupSubYBPITRInit   backupSubcommand = "yb-pitr-init"
	backupSubYBSnapExport backupSubcommand = "yb-snapshot-export"
	backupSubRestoreDrill backupSubcommand = "restore-drill"
)

// runBackupCommand routes the `./server ops backup [...]` family.
// The bare `./server ops backup` invocation runs a full snapshot.
// The `verify` subcommand operates on an existing backup directory.
func runBackupCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return runBackupRun(ctx, cfg)
	}
	sub := backupSubcommand(args[0])
	switch sub {
	case backupSubHelp:
		printBackupUsage()
		return nil
	case backupSubVerify:
		return runBackupVerifyCmd(ctx, cfg, args[1:])
	case backupSubBucketsInit:
		return runBackupBucketsInitCmd(ctx, cfg, args[1:])
	case backupSubYBPITRInit:
		return runBackupYBPITRInitCmd(ctx, cfg, args[1:])
	case backupSubYBSnapExport:
		return runBackupYBSnapshotExportCmd(ctx, cfg, args[1:])
	case backupSubRestoreDrill:
		return runBackupRestoreDrillCmd(ctx, cfg, args[1:])
	}
	printBackupUsage()
	return fmt.Errorf("unknown backup command %q", args[0])
}

func runBackupVerifyCmd(ctx context.Context, _ *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printBackupVerifyUsage()
		return nil
	}
	return RunBackupVerify(ctx, args[0])
}

func runBackupBucketsInitCmd(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) > 0 && args[0] == "help" {
		printBackupBucketsInitUsage()
		return nil
	}
	return RunBackupBucketsInit(ctx, cfg)
}

func runBackupYBPITRInitCmd(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) > 0 && args[0] == "help" {
		printBackupYBPITRInitUsage()
		return nil
	}
	return RunBackupYBPITRInit(ctx, cfg)
}

func runBackupYBSnapshotExportCmd(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) > 0 && args[0] == "help" {
		printBackupYBSnapshotExportUsage()
		return nil
	}
	return RunBackupYBSnapshotExport(ctx, cfg)
}

func runBackupRestoreDrillCmd(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) > 0 && args[0] == "help" {
		printBackupRestoreDrillUsage()
		return nil
	}
	return RunBackupRestoreDrill(ctx, cfg)
}

func printBackupUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup [verify|buckets-init|yb-pitr-init|yb-snapshot-export] [args]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  (none)              Run a full snapshot of FDB, Yugabyte, Temporal-DB, and Meilisearch")
	fmt.Fprintln(os.Stderr, "  verify <path>       Structural inventory check of an existing backup directory")
	fmt.Fprintln(os.Stderr, "  buckets-init        Idempotently create the SeaweedFS S3 backup buckets")
	fmt.Fprintln(os.Stderr, "  yb-pitr-init        Create the YugabyteDB point-in-time-recovery snapshot schedule")
	fmt.Fprintln(os.Stderr, "  yb-snapshot-export  Export a YugabyteDB distributed snapshot off-host to the object store")
}

func printBackupVerifyUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup verify <backup-dir>")
}

func printBackupBucketsInitUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup buckets-init")
}

func printBackupYBPITRInitUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup yb-pitr-init")
}

func printBackupYBSnapshotExportUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup yb-snapshot-export")
}

func printBackupRestoreDrillUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup restore-drill")
}
