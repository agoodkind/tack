package integration_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/migrations"
)

const (
	// migrateOutboxVersion is the migration that creates public.ops_outbox.
	// An --execute run writes its audit intent there before it migrates.
	migrateOutboxVersion = 5
	// migratePartialVersion is the --to target of the partial run.
	migratePartialVersion = 10
)

// migrateCommand runs the built server binary against one database.
type migrateCommand struct {
	binary string
	logDir string
	dsn    string
}

// TestMigrateReportsStateAndRefusesGuardedRuns runs the built `migrate`
// command against a new database. The dry run reports the state without
// creating the goose table, a refused guard leaves the version unchanged, and
// --to stops at its target.
func TestMigrateReportsStateAndRefusesGuardedRuns(t *testing.T) {
	command := migrateCommand{binary: buildMigrateServer(t), logDir: t.TempDir(), dsn: emptyLedgerDatabase(t)}
	embedded, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatalf("list the embedded migrations: %v", err)
	}

	empty := command.status(t)
	if empty.MigrationTablePresent || empty.AppliedVersion != 0 ||
		len(empty.Available) != len(embedded) || len(empty.Pending) != len(embedded) {
		t.Fatalf("dry run on an empty database = %+v, want no table and all %d migrations pending", empty, len(embedded))
	}
	if migrationTableExists(t, command.dsn) {
		t.Fatal("the dry run created the goose version table")
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
		if applied := command.status(t).AppliedVersion; applied != migrateOutboxVersion {
			t.Fatalf("after the refused run %v the database is at %d, want %d", flags, applied, migrateOutboxVersion)
		}
	}

	if output, err := command.run(t, "--execute",
		"--expect-from", strconv.Itoa(migrateOutboxVersion), "--to", strconv.Itoa(migratePartialVersion)); err != nil {
		t.Fatalf("migrate --to %d: %v\n%s", migratePartialVersion, err, output)
	}
	if applied := command.status(t).AppliedVersion; applied != migratePartialVersion {
		t.Fatalf("after --to %d the database is at %d", migratePartialVersion, applied)
	}
	if output, err := command.run(t, "--execute", "--expect-from", strconv.Itoa(migratePartialVersion)); err != nil {
		t.Fatalf("migrate to the highest version: %v\n%s", err, output)
	}
	final := command.status(t)
	if !final.MigrationTablePresent || final.AppliedVersion != empty.Highest() || len(final.Pending) != 0 {
		t.Fatalf("dry run after the full run = %+v, want version %d and nothing pending", final, empty.Highest())
	}
}

// buildMigrateServer builds cmd/server into a temporary directory.
func buildMigrateServer(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve the repository root: %v", err)
	}
	binary := filepath.Join(t.TempDir(), "tack-server")
	build := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-tags=fdb", "-o", binary, "./cmd/server")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/server: %v\n%s", err, output)
	}
	return binary
}

// run runs `migrate` with args in JSON output mode and returns stdout, or
// stdout and stderr together when the command fails. The audit outbox is the
// database under test.
func (command migrateCommand) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	process := exec.CommandContext(t.Context(), command.binary, append([]string{
		"--operator-service", "tack-migrate-acceptance", "--output", "json", "migrate",
	}, args...)...)
	process.Env = append(os.Environ(),
		"DATABASE_URL="+command.dsn, "AUDIT_OPERATOR_DSN="+command.dsn, "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(command.logDir, "tack.json"),
		"LOG_TEXT_FILE="+filepath.Join(command.logDir, "tack.log"))
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process.Stdout, process.Stderr = stdout, stderr
	if err := process.Run(); err != nil {
		return stdout.String() + stderr.String(), err
	}
	return stdout.String(), nil
}

// status runs the dry run and decodes stdout, which must be one JSON
// document.
func (command migrateCommand) status(t *testing.T) schemastatus.Status {
	t.Helper()
	output, err := command.run(t)
	if err != nil {
		t.Fatalf("migrate dry run: %v\n%s", err, output)
	}
	var envelope struct {
		Result schemastatus.Status `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode the dry run output: %v\n%s", err, output)
	}
	return envelope.Result
}

func migrationTableExists(t *testing.T, dsn string) bool {
	t.Helper()
	connection, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	defer func() { _ = connection.Close(t.Context()) }()
	var exists bool
	if err := connection.QueryRow(t.Context(),
		`SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&exists); err != nil {
		t.Fatalf("check for goose_db_version: %v", err)
	}
	return exists
}
