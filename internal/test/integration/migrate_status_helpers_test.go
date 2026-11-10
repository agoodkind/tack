package integration_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"

	"goodkind.io/tack/internal/schemastatus"
	"goodkind.io/tack/migrations"
)

// applyFutureMigration applies the embedded migrations and one no-op
// migration numbered above them through goose.
func applyFutureMigration(t *testing.T, dsn string) {
	t.Helper()
	future := fstest.MapFS{}
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		t.Fatalf("list the embedded migrations: %v", err)
	}

	for _, entry := range entries {
		contents, err := fs.ReadFile(migrations.FS, entry.Name())
		if err != nil {
			t.Fatalf("read migration %s: %v", entry.Name(), err)
		}

		future[entry.Name()] = &fstest.MapFile{Data: contents}
	}

	contents, err := os.ReadFile(filepath.Join("testdata", "migrate_future.sql"))
	if err != nil {
		t.Fatalf("read the future migration: %v", err)
	}

	future[strconv.Itoa(migrateFutureVersion)+"_future.sql"] = &fstest.MapFile{Data: contents}
	database, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}

	defer func() { _ = database.Close() }()
	provider, err := goose.NewProvider(goose.DialectPostgres, database, future)
	if err != nil {
		t.Fatalf("load the future migration set: %v", err)
	}

	if _, err := provider.Up(t.Context()); err != nil {
		t.Fatalf("apply migration %d: %v", migrateFutureVersion, err)
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
	build := exec.CommandContext(
		t.Context(),
		"go",
		"build",
		"-buildvcs=false",
		"-tags=fdb",
		"-o",
		binary,
		"./cmd/server",
	)
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cmd/server: %v\n%s", err, output)
	}

	return binary
}

// run runs `migrate` with args in JSON output mode and returns stdout, or
// stdout and stderr together when the command fails. Audit records use a separate ledger.
func (command migrateCommand) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	process := exec.CommandContext(
		t.Context(),
		command.binary,
		append(
			[]string{
				"--operator-service",
				command.service,
				"--output",
				"json",
				"migrate",
			},
			args...,
		)...,
	)
	process.Env = append(
		os.Environ(),
		"DATABASE_URL="+command.dsn,
		"AUDIT_OPERATOR_DSN="+command.auditDSN,
		"LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(command.logDir, "tack.json"),
		"LOG_TEXT_FILE="+filepath.Join(command.logDir, "tack.log"),
	)
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	process.Stdout, process.Stderr = stdout, stderr
	if err := process.Run(); err != nil {
		return stdout.String() + stderr.String(), err
	}

	return stdout.String(), nil
}

// status returns the decoded status and its compact JSON payload.
func (command migrateCommand) status(t *testing.T) (schemastatus.Status, []byte) {
	t.Helper()
	output, err := command.run(t)
	if err != nil {
		t.Fatalf("migrate dry run: %v\n%s", err, output)
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("decode the dry run output: %v\n%s", err, output)
	}

	var payload bytes.Buffer
	if err := json.Compact(&payload, envelope.Result); err != nil {
		t.Fatalf("compact the migration status: %v", err)
	}

	var status schemastatus.Status
	if err := json.Unmarshal(envelope.Result, &status); err != nil {
		t.Fatalf("decode the migration status: %v", err)
	}

	return status, payload.Bytes()
}

func migrationTableExists(t *testing.T, dsn string) bool {
	t.Helper()
	connection, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}

	defer func() { _ = connection.Close(t.Context()) }()
	var exists bool
	if err := connection.QueryRow(
		t.Context(),
		`SELECT to_regclass('goose_db_version') IS NOT NULL`,
	).Scan(
		&exists,
	); err != nil {
		t.Fatalf("check for goose_db_version: %v", err)
	}

	return exists
}
