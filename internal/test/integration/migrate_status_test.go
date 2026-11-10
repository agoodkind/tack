package integration_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/migrations"
)

const (
	// migrateOutboxVersion is the migration that creates public.ops_outbox.
	// An --execute run writes its audit intent there before it migrates.
	migrateOutboxVersion = 5
	// migratePartialVersion is the --to target of the partial run.
	migratePartialVersion = 10
	// migrateFutureVersion numbers a migration above every embedded one.
	migrateFutureVersion = 9999
)

// migrateCommand runs the built server binary with a migration target and an audit ledger.
type migrateCommand struct {
	binary   string
	logDir   string
	dsn      string
	auditDSN string
	service  string
}

// TestMigrateReportsStateAndRefusesGuardedRuns runs the built `migrate`
// command against a new database. The dry run reports the state without
// creating the goose table, a refused guard leaves the version unchanged, and
// --to stops at its target.
func TestMigrateReportsStateAndRefusesGuardedRuns(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	command := migrateCommand{
		binary:   buildMigrateServer(t),
		logDir:   t.TempDir(),
		dsn:      emptyLedgerDatabase(t),
		auditDSN: ledgerLoginDSN(t, ledger, "audit_operator"),
		service:  "tack-migrate-acceptance-" + uuid.NewString()[:8],
	}
	embedded, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("list the embedded migrations: %v", err)
	}

	started := clock.Now().UTC()
	empty, payload := command.status(t)
	if empty.MigrationTablePresent || empty.AppliedVersion != 0 ||
		len(empty.Available) != len(embedded) || len(empty.Pending) != len(embedded) {
		t.Fatalf(
			"dry run on an empty database = %+v, want no table and all %d migrations pending",
			empty,
			len(embedded),
		)
	}

	if migrationTableExists(t, command.dsn) {
		t.Fatal("the dry run created the goose version table")
	}

	stack := runtimeAuditStack{topic: "", admin: ledger.admin, reader: ledger.reader, recorder: nil}
	row := waitForActorRow(
		t,
		stack,
		string(audit.VerbDatabaseMigrate),
		cli.ServiceActorID(command.service),
		started,
	)
	requireReadRow(t, row, audit.VerbDatabaseMigrate, audit.OutcomeCompleted, "", payload)
	targets := schemastatus.MigrationTargets(empty.Available)
	manifest := (publicLedger{stack: stack, since: started}).manifestTargets(t, row)
	if !slices.Equal(manifest, targets) {
		t.Fatalf("migration status read targets = %+v, want %+v", manifest, targets)
	}

	manifestDigest, manifestParts, err := audit.ReadManifestDigest(targets)
	if err != nil {
		t.Fatalf("compute the migration status manifest: %v", err)
	}

	var targetCount uint64

	for range targets {
		targetCount++
	}

	if row.Context.Read.Count != targetCount || row.Context.Read.ManifestParts != manifestParts ||
		row.Context.Read.ManifestSHA256 != manifestDigest {
		t.Fatalf(
			"migration status read evidence = %+v, want %d targets and manifest %s",
			row.Context.Read,
			targetCount,
			manifestDigest,
		)
	}

	migrateLedgerTo(t, command.dsn, migrateOutboxVersion)

	refusals := [][]string{
		{"--expect-from", strconv.Itoa(migrateOutboxVersion - 1)},
		{"--to", strconv.Itoa(migrateOutboxVersion - 1)},
		{"--to", strconv.FormatInt(empty.Highest()+1, 10)},
	}
	for _, flags := range refusals {
		if output, err := command.run(t, append([]string{"--execute"}, flags...)...); err == nil {
			t.Fatalf("migrate --execute %v succeeded, want a refusal:\n%s", flags, output)
		}

		status, _ := command.status(t)
		if applied := status.AppliedVersion; applied != migrateOutboxVersion {
			t.Fatalf(
				"after the refused run %v the database is at %d, want %d",
				flags,
				applied,
				migrateOutboxVersion,
			)
		}
	}

	if output, err := command.run(
		t,
		"--execute",
		"--expect-from",
		strconv.Itoa(migrateOutboxVersion),
		"--to",
		strconv.Itoa(migratePartialVersion),
	); err != nil {
		t.Fatalf("migrate --to %d: %v\n%s", migratePartialVersion, err, output)
	}

	partial, _ := command.status(t)
	if applied := partial.AppliedVersion; applied != migratePartialVersion {
		t.Fatalf("after --to %d the database is at %d", migratePartialVersion, applied)
	}

	if output, err := command.run(
		t,
		"--execute",
		"--expect-from",
		strconv.Itoa(migratePartialVersion),
	); err != nil {
		t.Fatalf("migrate to the highest version: %v\n%s", err, output)
	}

	final, _ := command.status(t)
	if !final.MigrationTablePresent || final.AppliedVersion != empty.Highest() ||
		len(final.Pending) != 0 {
		t.Fatalf(
			"dry run after the full run = %+v, want version %d and nothing pending",
			final,
			empty.Highest(),
		)
	}

	applyFutureMigration(t, command.dsn)
	if output, err := command.run(t, "--execute"); err == nil {
		t.Fatalf("migrate --execute above the embedded set succeeded, want a refusal:\n%s", output)
	}

	logged, err := os.ReadFile(filepath.Join(command.logDir, "tack.json"))
	if err != nil || !bytes.Contains(logged, []byte("above the highest embedded version")) {
		t.Fatalf(
			"the command log does not record the refusal above the embedded set (read error %v)",
			err,
		)
	}

	future, _ := command.status(t)
	if applied := future.AppliedVersion; applied != migrateFutureVersion {
		t.Fatalf(
			"after the refused run the database is at %d, want %d",
			applied,
			migrateFutureVersion,
		)
	}
}
