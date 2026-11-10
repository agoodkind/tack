package integration_test

import (
	"bytes"
	_ "embed"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

//go:embed testdata/service_secret_ledger_user.sql
var serviceSecretLedgerUserQuery string

func TestServiceSecretLedgerAuthenticatesFromFile(t *testing.T) {
	name := "ledger-" + uuid.NewString()[:8] + "xx"
	ledger := testenv.StartEmptyLedger(t, name)
	path := filepath.Join(t.TempDir(), "database-url")
	if err := os.WriteFile(path, []byte(ledger.DSN), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DATABASE_URL_FILE", path)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.Connect(t.Context(), cfg.DatabaseURL)
	if err != nil {
		t.Fatal("the file-backed ledger credential did not authenticate")
	}
	defer connection.Close(t.Context())
	var user string
	if err := connection.QueryRow(t.Context(), serviceSecretLedgerUserQuery).Scan(&user); err != nil || user != "yugabyte" {
		t.Fatal("the ledger did not execute an authenticated query")
	}
	parsed, err := pgx.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		t.Fatal("the ledger DSN could not be parsed")
	}
	parsed.Password = "incorrect-fixture-password"
	unexpected, err := pgx.ConnectConfig(t.Context(), parsed)
	if err == nil {
		unexpected.Close(t.Context())
		t.Fatal("the ledger accepted an incorrect password")
	}
	docker, err := client.New(client.WithHost(client.DefaultDockerHost))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	missing, err := docker.ExecCreate(t.Context(), name, client.ExecCreateOptions{
		Cmd: []string{"env", "-u", "YSQL_PASSWORD_FILE", "bash", "/opt/tack/yugabyte-healthcheck.sh"}, AttachStderr: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := docker.ExecAttach(t.Context(), missing.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if _, err := io.Copy(&output, attached.Reader); err != nil {
		t.Fatal(err)
	}
	attached.Close()
	result, err := docker.ExecInspect(t.Context(), missing.ID, client.ExecInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == 0 || !strings.Contains(output.String(), "YSQL_PASSWORD_FILE is required") {
		t.Fatal("the initialized ledger accepted a missing password file reference")
	}
	inspected, err := docker.ContainerInspect(t.Context(), name, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	referenced := false
	for _, setting := range inspected.Container.Config.Env {
		if strings.HasPrefix(setting, "YSQL_PASSWORD=") || strings.HasPrefix(setting, "PGPASSWORD=") {
			t.Fatal("the engine exposes a plaintext password in Docker configuration")
		}
		referenced = referenced || setting == "YSQL_PASSWORD_FILE=/run/secrets/YUGABYTE_PASSWORD"
	}
	if !referenced || os.Getenv("DATABASE_URL") != "" {
		t.Fatal("the service did not retain its file reference boundary")
	}
}
