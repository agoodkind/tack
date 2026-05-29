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
	backupSubHelp        backupSubcommand = "help"
	backupSubVerify      backupSubcommand = "verify"
	backupSubRestoreTest backupSubcommand = "restore-test"
	backupSubBucketsInit backupSubcommand = "buckets-init"
)

// runBackupCommand routes the `./server ops backup [...]` family.
// The bare `./server ops backup` invocation runs a full snapshot.
// Subcommands `verify` and `restore-test` operate on an existing
// backup directory.
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
	case backupSubRestoreTest:
		return runBackupRestoreTestCmd(ctx, cfg, args[1:])
	case backupSubBucketsInit:
		return runBackupBucketsInitCmd(ctx, cfg, args[1:])
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

func runBackupRestoreTestCmd(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] == "help" {
		printBackupRestoreTestUsage()
		return nil
	}
	return RunBackupRestoreTest(ctx, cfg, args[0])
}

func runBackupBucketsInitCmd(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) > 0 && args[0] == "help" {
		printBackupBucketsInitUsage()
		return nil
	}
	return RunBackupBucketsInit(ctx, cfg)
}

func printBackupUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup [verify|restore-test|buckets-init] [args]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "subcommands:")
	fmt.Fprintln(os.Stderr, "  (none)         Run a full snapshot of FDB, Yugabyte, Temporal-DB, and Meilisearch")
	fmt.Fprintln(os.Stderr, "  verify <path>  Structural inventory check of an existing backup directory")
	fmt.Fprintln(os.Stderr, "  restore-test <path>")
	fmt.Fprintln(os.Stderr, "                 End-to-end restore replay against scratch containers")
	fmt.Fprintln(os.Stderr, "  buckets-init   Idempotently create the SeaweedFS S3 backup buckets")
}

func printBackupVerifyUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup verify <backup-dir>")
}

func printBackupRestoreTestUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup restore-test <backup-dir>")
}

func printBackupBucketsInitUsage() {
	fmt.Fprintln(os.Stderr, "usage: ./server ops backup buckets-init")
}
