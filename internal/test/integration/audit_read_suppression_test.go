package integration_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/testenv"
)

const (
	readSuppressionNotarizationRef = "audit/notarizations"
	readSuppressionReportKey       = "audit.signers"
	readSuppressionNoGrantRole     = "audit_reader"
	readSuppressionAttachRef       = "attach/verdict"
	readSuppressionAttachReport    = "attached verdict report"
	readSuppressionAttachFailure   = "the attached check failed"
)

func writeVerdictSigningKey(t *testing.T) (string, string, ed25519.PrivateKey) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the signing key: %v", err)
	}

	encoded, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatalf("encode the signing key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "audit-signing.pem")
	if err := os.WriteFile(
		path,
		pem.EncodeToMemory(&pem.Block{
			Type:  "PRIVATE KEY",
			Bytes: encoded,
		}),
		0o600,
	); err != nil {
		t.Fatalf("write the signing key: %v", err)
	}

	return path, audit.KeyIdentifier(public), private
}

func TestAuditSignersRecordsTheVerdictAfterTheReport(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	keyPath, keyID, _ := writeVerdictSigningKey(t)

	otherPublic, otherPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate the other key: %v", err)
	}

	root := make([]byte, 32)
	if _, err := rand.Read(root); err != nil {
		t.Fatalf("create the Merkle root: %v", err)
	}

	signedAt := clock.Now()
	if _, err := ledger.admin.Exec(
		t.Context(),
		readTestSQL(t, "audit_signers_insert_notarization.sql"),
		uuid.Must(uuid.NewV7()),
		signedAt,
		root,
		ed25519.Sign(otherPrivate, root),
		audit.KeyIdentifier(otherPublic),
	); err != nil {
		t.Fatalf("insert a notarization signed outside the set: %v", err)
	}

	since := signedAt.Add(-time.Minute).UTC().Format(time.RFC3339)
	operatorID, run := runReadLifecycleCommand(
		t,
		ledger,
		"audit",
		"signers",
		"--since",
		since,
		"--pub",
		keyPath,
		"--signers",
		keyID,
	)

	var exitErr *exec.ExitError
	if !errors.As(run.err, &exitErr) || exitErr.ExitCode() != 1 ||
		!strings.Contains(run.stdout, readSuppressionReportKey) {
		t.Fatalf(
			"audit signers with a signer outside the set = %v, want exit code 1 with the report printed\n%s",
			run.err,
			run.output(),
		)
	}

	output := decodeReadLifecycleOutput(t, run)
	requireVerdictReadRow(
		t,
		awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditSignersVerified),
		referenceTarget(readSuppressionNotarizationRef),
		output.Printed,
		nil,
		"audit signers",
	)
}

func TestAttachReadWritesTheReportOfAVerdictAfterTheFinalEvent(t *testing.T) {
	ledger := startNodeAuditLedger(t)

	var printed bytes.Buffer

	factory := cli.System(ledger.cfg)
	factory.Out, factory.Err = &printed, &bytes.Buffer{}
	factory.SetAuditOutbox(audit.NewPoolOutbox(ledger.admin))
	factory.SetAuditReads(
		audit.NewPoolReadLifecycle(rolePool(t, testenv.Ledger(t), "audit_operator")),
	)

	target := audit.ExternalTarget(readSuppressionAttachRef)
	spec := audit.Spec{
		Verb:  string(audit.VerbAuditVerify),
		Reads: true,
		Class: audit.OperationRead,
		Targets: func(context.Context, json.RawMessage) ([]audit.Target, error) {
			return []audit.Target{
				target,
			}, nil
		},
		SafeInput: func(json.RawMessage) (json.RawMessage, error) { return nil, nil },
	}
	command := &cobra.Command{Use: "attach-verdict", SilenceErrors: true, SilenceUsage: true}
	factory.RegisterGlobalFlags(command)
	factory.SetOperatorIdentitySource(cli.NewOperatorSource(factory))
	clispec.AttachRead(
		command,
		factory,
		spec,
		func() (json.RawMessage, error) { return json.RawMessage("{}"), nil },
		func(context.Context) (audit.OperatorReadOutput, error) {
			output := audit.OperatorReadOutput{
				Payload: []byte(readSuppressionAttachReport),
				Format:  "text/plain",
				Targets: nil,
			}

			return output, &audit.ReadVerdictError{Err: errors.New(readSuppressionAttachFailure)}
		},
	)

	operatorID := uuid.Must(uuid.NewV7())

	command.SetContext(t.Context())
	command.SetArgs([]string{
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		"attach-verdict@example.test",
	})

	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), readSuppressionAttachFailure) ||
		!strings.Contains(printed.String(), readSuppressionAttachReport) {
		t.Fatalf(
			"attached read verdict = %v with output %q, want the verdict error and the printed report",
			err,
			printed.String(),
		)
	}

	row := awaitReadLifecycleRow(t, ledger, operatorID, audit.VerbAuditVerify)
	requireReadRowShape(t, row, audit.OutcomeFailed, referenceTarget(readSuppressionAttachRef))

	if row.Error == nil || row.Error.Code != "check_failed" || row.Context.Read == nil {
		t.Fatalf(
			"row %s error %+v read %+v, want check_failed with read evidence",
			row.EventID,
			row.Error,
			row.Context.Read,
		)
	}

	if row.Context.Read.PayloadSHA256 != payloadDigest(printed.Bytes()) {
		t.Fatalf(
			"row %s fingerprint %s does not match the SHA-256 of the printed bytes %q",
			row.EventID,
			row.Context.Read.PayloadSHA256,
			printed.String(),
		)
	}
}

func TestAuditReadAdmissionDenialSuppressesTheReport(t *testing.T) {
	ledger := startNodeAuditLedger(t)
	now := clock.Now()
	operatorID := uuid.Must(uuid.NewV7())

	denied := builtAuditCommandAs(
		t,
		ledgerLoginDSN(t, ledger, readSuppressionNoGrantRole),
		ledger.cfg.AuditReaderDSN,
		"--execute",
		"--operator-id",
		operatorID.String(),
		"--operator-email",
		"read-suppression@example.test",
		"--output",
		"json",
		"audit",
		"last-change",
		"--org",
		uuid.Must(uuid.NewV7()).String(),
		"--entity-id",
		uuid.Must(uuid.NewV7()).String(),
		"--oldest",
		now.Add(-time.Hour).UTC().Format(time.RFC3339),
		"--latest",
		now.Add(time.Hour).UTC().Format(time.RFC3339),
	)
	if denied.err == nil || denied.stdout != "" {
		t.Fatalf(
			"audit last-change without the read admission grant = %v with output %q, want an error and no output\n%s",
			denied.err,
			denied.stdout,
			denied.output(),
		)
	}

	if events := readOutboxEvents(t, ledger.admin, audit.VerbAuditRead, operatorID); len(
		events,
	) != 0 {
		t.Fatalf("the refused admission left %d outbox events, want none: %+v", len(events), events)
	}
}
