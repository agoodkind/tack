//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/testenv"
)

// topLevelEnv groups the server binary, process environment, ledger access, log
// path, and test event identifiers for command coverage.
type topLevelEnv struct {
	binary      string
	environment []string
	admin       *pgxpool.Pool
	logText     string
	orgID       uuid.UUID
	actorID     uuid.UUID
	eventID     uuid.UUID
}

// startTopLevelEnv builds the server binary and prepares a separate
// FoundationDB cluster, ledger access, and a signing key. It configures file
// logging and records a test actor's event for audit command cases.
func startTopLevelEnv(t *testing.T) *topLevelEnv {
	t.Helper()
	ledger := testenv.Ledger(t)
	cluster := testenv.FreshFoundationDB(t)
	keyPath, keyID := writeCoverageSigningKey(t)
	logs := t.TempDir()
	environment := append(os.Environ(),
		"DATABASE_URL="+ledger, "FDB_CLUSTER_FILE="+cluster, "ENV=development", "PORT=0",
		"AUDIT_ALLOW_UNRECORDED=true", "AUDIT_OPERATOR_DSN="+ledger, "AUDIT_READER_DSN="+ledger,
		"AUDIT_REDACTOR_DSN="+ledger, "AUDIT_SIGNING_KEY_PATH="+keyPath, "AUDIT_VALID_SIGNERS="+keyID,
		"SEED_EMAIL=coverage-seed@example.test", "SEED_NAME=Coverage Seed", "LOG_DISABLE_STDOUT=true",
		"LOG_JSON_FILE="+filepath.Join(logs, "tack.json"), "LOG_TEXT_FILE="+filepath.Join(logs, "tack.log"),
	)
	admin := ledgerAdminPool(t)
	orgID, actorID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	eventID := recordCoverageActorEvent(t, ledger, orgID, actorID)
	return &topLevelEnv{
		binary: buildMigrateServer(t), environment: environment, admin: admin, logText: filepath.Join(logs, "tack.log"),
		orgID: orgID, actorID: actorID, eventID: eventID,
	}
}

// logCount counts occurrences of the supplied text in the server's text log. It
// returns zero if reading the log fails.
func (env *topLevelEnv) logCount(t *testing.T, text string) int {
	t.Helper()
	contents, err := os.ReadFile(env.logText)
	if err != nil {
		return 0
	}
	return bytes.Count(contents, []byte(text))
}

// command prepares a server process with execution enabled and the supplied
// operator identity. It collects standard output and standard error in one
// buffer.
func (env *topLevelEnv) command(t *testing.T, operatorID uuid.UUID, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	process := exec.CommandContext(t.Context(), env.binary, append([]string{
		"--execute", "--operator-id", operatorID.String(),
		"--operator-email", "coverage@example.test", "--operator-name", "Coverage Test",
	}, args...)...)
	process.Env = env.environment
	var output bytes.Buffer
	process.Stdout, process.Stderr = &output, &output
	return process, &output
}

// run executes a command with a new operator ID and returns that ID, the
// combined output, and any execution error.
func (env *topLevelEnv) run(t *testing.T, args ...string) (uuid.UUID, string, error) {
	t.Helper()
	operatorID := uuid.Must(uuid.NewV7())
	process, output := env.command(t, operatorID, args...)
	err := process.Run()
	return operatorID, output.String(), err
}

// recordCoverageActorEvent records a successful node read for the supplied
// organization and actor and returns its event ID.
func recordCoverageActorEvent(t *testing.T, ledger string, orgID, actorID uuid.UUID) uuid.UUID {
	t.Helper()
	recorder, err := audit.NewYBRecorder(t.Context(), ledger)
	if err != nil {
		t.Fatalf("Audit recorder creation failed; the command cases require a recorder for the test event: %v", err)
	}
	defer recorder.Close()
	eventID := uuid.Must(uuid.NewV7())
	err = recorder.Record(t.Context(), audit.Event{
		Verb: string(audit.VerbNodeRead), EventID: eventID,
		Actor:   audit.Actor{Type: audit.ActorUser, ID: actorID, Email: "coverage-actor@example.test", Name: "Coverage Actor"},
		Entity:  audit.Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context: audit.EventContext{OrgID: orgID, Source: audit.SourceMCP},
		Outcome: audit.OutcomeOK,
	})
	if err != nil {
		t.Fatalf("Recording the test actor's event failed; the audit command cases require that ledger event: %v", err)
	}
	return eventID
}

// writeCoverageSigningKey generates an Ed25519 signing key and writes the
// private key to a temporary PEM file. It returns the file path and public key
// identifier.
func writeCoverageSigningKey(t *testing.T) (string, string) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("Ed25519 key generation failed; the audit command cases require a signing key: %v", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatalf("Private key encoding failed; the audit command cases require a PKCS #8 key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "audit-signing.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}), 0o600); err != nil {
		t.Fatalf("Writing the signing key failed; the audit command cases require a PEM key file: %v", err)
	}
	return path, audit.KeyIdentifier(public)
}
