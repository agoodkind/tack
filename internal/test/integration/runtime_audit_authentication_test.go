package integration_test

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/testenv"
)

const (
	// invalidPasswordState is the SQLSTATE text that YugabyteDB returns when
	// the operator outbox login uses a wrong password.
	invalidPasswordState = "SQLSTATE 28P01"
	// missingDatabaseState is the SQLSTATE text of a connection to a database
	// that does not exist.
	missingDatabaseState = "SQLSTATE 3D000"
)

// TestRuntimeAuditAuthenticationRefusal runs ordinary commands with the
// built server binary. AUDIT_OPERATOR_DSN uses a wrong password. The operator
// outbox and the read lifecycle store fail authentication with it.
// DATABASE_URL points to a database that does not exist. The command and the
// audit infrastructure probe both connect to DATABASE_URL. Either of them
// logs SQLSTATE 3D000 when it runs. The test requires each command to fail
// with the authentication refusal, to log only the authentication failure,
// and to print no result.
func TestRuntimeAuditAuthenticationRefusal(t *testing.T) {
	ledgerDSN := testenv.Ledger(t)
	operatorDSN := changedDSN(t, ledgerDSN, func(parsed *url.URL) {
		parsed.User = url.UserPassword(parsed.User.Username(), "wrong-"+uuid.NewString())
	})
	missingDSN := changedDSN(t, ledgerDSN, func(parsed *url.URL) {
		parsed.Path = "/tack_missing_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	})
	binary := buildMigrateServer(t)
	for _, current := range []struct {
		name    string
		args    []string
		refusal string
	}{
		{name: "read", args: []string{"migrate"}, refusal: "record read access"},
		{name: "external", args: []string{"--execute", "migrate"}, refusal: "record command intent"},
		{name: "generated_preview", args: tokenCreateArgs(), refusal: "record read access"},
		{name: "generated_command", args: append([]string{"--execute"}, tokenCreateArgs()...), refusal: "record command intent"},
	} {
		t.Run(current.name, func(t *testing.T) {
			logDir := t.TempDir()
			arguments := append([]string{
				"--operator-id", uuid.Must(uuid.NewV7()).String(), "--operator-email", "authentication@example.invalid",
			}, current.args...)
			process := exec.CommandContext(context.WithoutCancel(t.Context()), binary, arguments...)
			process.Env = append(os.Environ(), "DATABASE_URL="+missingDSN, "AUDIT_OPERATOR_DSN="+operatorDSN,
				"LOG_DISABLE_STDOUT=true", "LOG_JSON_FILE="+filepath.Join(logDir, "tack.json"),
				"LOG_TEXT_FILE="+filepath.Join(logDir, "tack.log"))
			output, err := process.CombinedOutput()
			if err == nil {
				t.Fatalf("%v succeeded after the failed authentication:\n%s", current.args, output)
			}
			logged, readErr := os.ReadFile(filepath.Join(logDir, "tack.log"))
			if readErr != nil {
				t.Fatalf("reading the command log failed: %v", readErr)
			}
			text := string(logged)
			if !strings.Contains(text, current.refusal) || !strings.Contains(text, invalidPasswordState) {
				t.Fatalf("%v did not log the %q refusal with %s:\n%s", current.args, current.refusal, invalidPasswordState, text)
			}
			if strings.Contains(text, missingDatabaseState) {
				t.Fatalf("%v connected to DATABASE_URL, and the command or the probe ran:\n%s", current.args, text)
			}
			if strings.Contains(string(output), "{") || strings.Contains(string(output), "migration") {
				t.Fatalf("%v printed a result after the failed authentication:\n%s", current.args, output)
			}
		})
	}
}

// tokenCreateArgs returns the arguments of ops auth token-create, a generated
// command that connects to DATABASE_URL in its dry run and in its run.
func tokenCreateArgs() []string {
	return []string{"ops", "auth", "token-create", "--email", "holder@example.invalid", "--label", "authentication-refusal"}
}

// changedDSN returns dsn after change modifies its parsed form.
func changedDSN(t *testing.T, dsn string, change func(*url.URL)) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parsing the ledger DSN failed: %v", err)
	}
	change(parsed)
	return parsed.String()
}
