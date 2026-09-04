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

// TestAuditSignersCommandRefusesWithoutASet pins the fail-closed default: with
// neither --signers nor AUDIT_VALID_SIGNERS the command stops before it opens
// anything, because an empty allowlist would accept nothing and a skipped
// check would accept everything.
func TestAuditSignersCommandRefusesWithoutASet(t *testing.T) {
	factory := &cli.Factory{Cfg: nil, In: nil, Out: os.Stdout, Err: os.Stderr}
	operation := auditSignersOp(factory)
	input := auditSignersInput{InputMarker: clispec.InputMarker{}, Since: "", Signers: "", Pub: ""}

	err := operation.Run(context.Background(), input, clispec.NewCLISink(factory))
	if err == nil || !strings.Contains(err.Error(), "no valid signer set") {
		t.Fatalf("err = %v, want the refusal to run without a signer set", err)
	}
}

// TestAuditVerifyCommandRejectsASignerOutsideTheSet runs the real verify
// command against a bundle whose manifest names a signer the given set does
// not, and asserts the verdict names that.
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
	input := auditVerifyInput{InputMarker: clispec.InputMarker{}, Bundle: bundleDir, Pub: keyPath, Signers: "ed25519:0000000000000000"}

	err := operation.Run(context.Background(), input, clispec.NewCLISink(factory))
	if err == nil || !strings.Contains(err.Error(), "outside the valid signer set") {
		t.Fatalf("err = %v, want the signer rejection", err)
	}
}
