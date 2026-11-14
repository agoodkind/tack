package servercli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// TestAuditSignersCommandRefusesWithoutASet checks that the command requires
// --signers or AUDIT_VALID_SIGNERS before opening dependencies.
func TestAuditSignersCommandRefusesWithoutASet(t *testing.T) {
	factory := &cli.Factory{Cfg: nil, In: nil, Out: os.Stdout, Err: os.Stderr}
	operation := auditSignersOp(factory)
	input := auditSignersInput{
		InputMarker:     clispec.InputMarker{},
		Since:           "",
		Signers:         "",
		Pub:             "",
		AllowUnverified: false,
	}

	err := operation.Run(context.Background(), input, clispec.NewCLISink(factory))
	if err == nil || !strings.Contains(err.Error(), "no valid signer set") {
		t.Fatalf("err = %v, want the refusal to run without a signer set", err)
	}
}

// TestAuditSignersCommandRefusesWithoutALocalKey pins that a set alone is not
// enough: with no key to verify signatures the command stops rather than
// report every row as unverified and exit clean.
func TestAuditSignersCommandRefusesWithoutALocalKey(t *testing.T) {
	factory := &cli.Factory{Cfg: nil, In: nil, Out: os.Stdout, Err: os.Stderr}
	operation := auditSignersOp(factory)
	input := auditSignersInput{
		InputMarker:     clispec.InputMarker{},
		Since:           "",
		Signers:         "ed25519:0000000000000000",
		Pub:             "",
		AllowUnverified: false,
	}

	err := operation.Run(context.Background(), input, clispec.NewCLISink(factory))
	if err == nil || !strings.Contains(err.Error(), "no local signing key") {
		t.Fatalf("err = %v, want the refusal to run without a local key", err)
	}
}

// TestAuditVerifyCommandRefusesWithoutASet pins that bundle verification no
// longer runs on the signature alone: with neither --signers nor
// AUDIT_VALID_SIGNERS the command refuses before it reads the bundle.
func TestAuditVerifyCommandRefusesWithoutASet(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeVerifyTestKey(t, dir)
	factory := &cli.Factory{Cfg: nil, In: nil, Out: os.Stdout, Err: os.Stderr}
	input := auditVerifyInput{
		InputMarker: clispec.InputMarker{},
		Bundle:      filepath.Join(dir, "absent"),
		Pub:         keyPath,
		Signers:     "",
	}

	err := auditVerifyOp(factory).Run(context.Background(), input, clispec.NewCLISink(factory))
	if err == nil || !strings.Contains(err.Error(), "no valid signer set") {
		t.Fatalf("err = %v, want the refusal to verify without a signer set", err)
	}
}

// TestAuditVerifyCommandRejectsASignerOutsideTheSet checks that verify rejects
// a bundle when its manifest signer does not belong to the supplied set.
func TestAuditVerifyCommandRejectsASignerOutsideTheSet(t *testing.T) {
	dir := t.TempDir()
	keyPath := writeVerifyTestKey(t, dir)
	bundleDir := filepath.Join(dir, "bundle")
	if err := os.MkdirAll(bundleDir, 0o750); err != nil {
		t.Fatalf("create bundle dir: %v", err)
	}

	writeMismatchedBundle(t, bundleDir, keyPath)

	factory := &cli.Factory{Cfg: nil, In: nil, Out: os.Stdout, Err: os.Stderr}
	operation := auditVerifyOp(factory)
	input := auditVerifyInput{
		InputMarker: clispec.InputMarker{},
		Bundle:      bundleDir,
		Pub:         keyPath,
		Signers:     "ed25519:0000000000000000",
	}

	err := operation.Run(context.Background(), input, clispec.NewCLISink(factory))
	if err == nil || !strings.Contains(err.Error(), "outside the valid signer set") {
		t.Fatalf("err = %v, want the signer rejection", err)
	}
}
