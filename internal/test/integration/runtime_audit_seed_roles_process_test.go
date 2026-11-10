package integration_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
)

type seedRolesProcessFiles struct {
	directory     string
	passwordFiles []string
	emptyFile     string
	environment   []string
	secrets       []string
}

func seedRolesProcessFixture(
	t *testing.T,
	databaseDSN string,
	operatorDSN string,
	passwords []string,
) seedRolesProcessFiles {
	t.Helper()
	directory := t.TempDir()
	databaseFile := filepath.Join(directory, "database")
	operatorFile := filepath.Join(directory, "operator")
	emptyFile := filepath.Join(directory, "empty")
	writeProtectedRoleFile(t, databaseFile, databaseDSN)
	writeProtectedRoleFile(t, operatorFile, operatorDSN)
	writeProtectedRoleFile(t, emptyFile, "")
	names := []string{
		"AUDIT_WRITER_PASSWORD",
		"AUDIT_READER_PASSWORD",
		"AUDIT_REDACTOR_PASSWORD",
		"AUDIT_OPERATOR_PASSWORD",
		"TACK_APP_PASSWORD",
		"TACK_MIGRATOR_PASSWORD",
	}
	files := make([]string, len(names))
	environment := []string{
		"HOME=" + directory,
		"PATH=/usr/bin:/bin",
		"DATABASE_URL_FILE=" + databaseFile,
		"AUDIT_OPERATOR_DSN_FILE=" + operatorFile,
		"LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE=" + filepath.Join(directory, "process.json"),
		"LOG_TEXT_FILE=" + filepath.Join(directory, "process.log"),
	}

	for index, name := range names {
		files[index] = filepath.Join(directory, name)
		writeProtectedRoleFile(t, files[index], passwords[index])
		environment = append(environment, name+"_FILE="+files[index])
	}

	databaseURL, err := url.Parse(databaseDSN)
	if err != nil {
		t.Fatal("parse the disposable database connection")
	}

	databasePassword, present := databaseURL.User.Password()
	if !present || databasePassword == "" {
		t.Fatal("the disposable database connection lacks a password")
	}

	return seedRolesProcessFiles{
		directory:     directory,
		passwordFiles: files,
		emptyFile:     emptyFile,
		environment:   environment,
		secrets: append(
			append([]string{}, passwords...),
			databaseDSN,
			operatorDSN,
			databasePassword,
		),
	}
}

func syntheticRolePassword(t *testing.T) string {
	t.Helper()
	value := make([]byte, 24)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate a synthetic password: %v", err)
	}

	return hex.EncodeToString(value)
}

func writeProtectedRoleFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatalf("write a protected fixture file: %v", err)
	}
}

func runSeedRolesProcess(
	t *testing.T,
	binary string,
	fixture seedRolesProcessFiles,
	actor uuid.UUID,
	overrides []string,
	beforeWait func(),
) error {
	t.Helper()
	process := exec.CommandContext(
		context.WithoutCancel(t.Context()),
		binary,
		"--execute",
		"--operator-id",
		actor.String(),
		"--operator-email",
		"rotation@example.invalid",
		"ops",
		"audit",
		"seed-roles",
	)
	process.Env = append(append([]string{}, fixture.environment...), overrides...)
	var output bytes.Buffer
	process.Stdout, process.Stderr = &output, &output
	if err := process.Start(); err != nil {
		return err
	}

	finished := false
	defer func() {
		if !finished {
			_ = process.Process.Kill()
			_ = process.Wait()
		}
	}()
	if beforeWait != nil {
		beforeWait()
	}

	err := process.Wait()
	finished = true
	assertRoleSecretsAbsent(t, output.Bytes(), fixture.secrets...)

	for _, name := range []string{"process.json", "process.log"} {
		logged, readErr := os.ReadFile(filepath.Join(fixture.directory, name))
		if readErr != nil {
			t.Fatalf("read the process log: %v", readErr)
		}

		assertRoleSecretsAbsent(t, logged, fixture.secrets...)
	}

	return err
}

func assertRoleSecretsAbsent(t *testing.T, data []byte, secrets ...string) {
	t.Helper()

	for _, secret := range secrets {
		if secret != "" && bytes.Contains(data, []byte(secret)) {
			t.Fatal("process output or audit evidence contains a synthetic secret")
		}
	}
}

func checkSeedRolesDryRun(
	t *testing.T,
	binary string,
	fixture seedRolesProcessFiles,
	actor uuid.UUID,
	admin *pgxpool.Pool,
) {
	t.Helper()
	dryRun := exec.CommandContext(
		t.Context(),
		binary,
		"--operator-id",
		actor.String(),
		"--operator-email",
		"rotation@example.invalid",
		"ops",
		"audit",
		"seed-roles",
	)

	for _, entry := range fixture.environment {
		if !strings.HasPrefix(entry, "AUDIT_OPERATOR_PASSWORD_FILE=") {
			dryRun.Env = append(dryRun.Env, entry)
		}
	}

	dryOutput, dryErr := dryRun.CombinedOutput()
	assertRoleSecretsAbsent(t, dryOutput, fixture.secrets...)
	if dryErr != nil {
		t.Fatal("the seed-roles dry run required an operator password")
	}

	if events := outboxEventsByQuery(
		t,
		admin,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbAuditRolesSeed),
		actor.String(),
	); len(events) != 0 {
		t.Fatal("the seed-roles dry run recorded an execution event")
	}
}
