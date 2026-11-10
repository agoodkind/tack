package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// TestAuditSignersCommandRefusesWithoutASet checks the missing-set error
// when neither --signers nor AUDIT_VALID_SIGNERS supplies a set. The command
// checks the set before opening the key or reader. An empty allowlist cannot
// accept any identifier; skipping the check would permit every identifier.
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

// TestAuditSignersCommandRefusesWithoutALocalKey supplies a signer set and
// checks the missing-key error. The command refuses verification without a
// local key. Matching identifiers alone cannot verify signatures or justify
// a successful report.
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

// TestAuditVerifyCommandRefusesWithoutASet checks the missing-set error with
// a local key and an absent bundle path. Neither --signers nor
// AUDIT_VALID_SIGNERS supplies a set. The command refuses before reading the
// bundle. This regression protects the change from signature-only bundle
// verification to verification with a required signer set.
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

// TestAuditVerifyCommandRejectsASignerOutsideTheSet runs the verify command
// against a bundle with a manifest signer outside the supplied set. The test
// checks that the error identifies rejection outside the valid signer set.
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
