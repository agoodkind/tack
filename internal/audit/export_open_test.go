// The verifier opens the rows file that the manifest selects before it can
// finish checking the bundle. These tests check which directory entries the
// verifier accepts at that filename.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

// exportedTestBundle writes a real bundle and returns its directory,
// verification key, and the path of the rows file that its manifest selects.
func exportedTestBundle(t *testing.T) (string, ed25519.PublicKey, string) {
	t.Helper()
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	manifest, err := Export(context.Background(), exportTestRows(t, orgID, 12, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	rowsPath := filepath.Join(dir, manifest.EventsFile)
	if _, err := os.Stat(rowsPath); err != nil {
		t.Fatalf("the export did not leave the rows its manifest names: %v", err)
	}
	return dir, publicKey, rowsPath
}

// TestTheVerifierRefusesRowsThatAreASymlink checks that the verifier refuses a
// symbolic link at the manifest's rows filename. A filename check restricts the
// name to the bundle directory without establishing the entry's type. A
// symbolic link can select an attacker-chosen file outside that directory while
// the report still identifies the bundle.
//
// The test links to a byte-for-byte copy of the real rows. Those bytes pass the
// digest and signature checks. Refusing the link itself must prevent a clean
// report for the external file.
func TestTheVerifierRefusesRowsThatAreASymlink(t *testing.T) {
	dir, publicKey, rowsPath := exportedTestBundle(t)

	body, err := os.ReadFile(rowsPath)
	if err != nil {
		t.Fatalf("read the published rows: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "rows-outside-the-bundle.jsonl")
	if err := os.WriteFile(outside, body, 0o600); err != nil {
		t.Fatalf("place the rows outside the bundle: %v", err)
	}
	if err := os.Remove(rowsPath); err != nil {
		t.Fatalf("remove the published rows: %v", err)
	}
	if err := os.Symlink(outside, rowsPath); err != nil {
		t.Fatalf("plant the symlink: %v", err)
	}

	report, err := VerifyBundle(dir, publicKey)
	if err == nil {
		t.Fatalf("the verifier followed a symlink out of the bundle and reported %+v", report)
	}
	if !strings.Contains(err.Error(), "verify jsonl") {
		t.Fatalf("err = %v, want the rows open refused", err)
	}
}

// TestTheVerifierRefusesRowsThatAreANamedPipe checks that opening the rows file
// cannot block indefinitely on a named pipe. A blocking open(2) call waits
// without a timeout when the pipe lacks a writer. A pipe at the manifest's rows
// filename can prevent verification from returning a verdict.
//
// The test bounds its wait to detect this regression without hanging the suite.
func TestTheVerifierRefusesRowsThatAreANamedPipe(t *testing.T) {
	dir, publicKey, rowsPath := exportedTestBundle(t)

	if err := os.Remove(rowsPath); err != nil {
		t.Fatalf("remove the published rows: %v", err)
	}
	if err := syscall.Mkfifo(rowsPath, 0o600); err != nil {
		t.Skipf("this filesystem cannot hold a named pipe: %v", err)
	}

	type verdict struct {
		report *VerifyReport
		err    error
	}
	done := make(chan verdict, 1)
	go func() {
		report, err := VerifyBundle(dir, publicKey)
		done <- verdict{report: report, err: err}
	}()

	select {
	case got := <-done:
		if got.err == nil {
			t.Fatalf("the verifier read a named pipe as a bundle and reported %+v", got.report)
		}
		if !strings.Contains(got.err.Error(), "verify jsonl") {
			t.Fatalf("err = %v, want the rows open refused", got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the verifier is still inside the rows open after 10s: a planted pipe stops it reaching any verdict")
	}
}

// TestTheVerifierRefusesRowsThatAreADirectory checks that the verifier refuses
// a directory at the manifest's rows filename. An attacker can create that
// entry without special privileges. Depending on the system, a directory read
// can appear empty or fail; neither outcome verifies a rows file.
func TestTheVerifierRefusesRowsThatAreADirectory(t *testing.T) {
	dir, publicKey, rowsPath := exportedTestBundle(t)

	if err := os.Remove(rowsPath); err != nil {
		t.Fatalf("remove the published rows: %v", err)
	}
	if err := os.Mkdir(rowsPath, 0o700); err != nil {
		t.Fatalf("plant the directory: %v", err)
	}

	report, err := VerifyBundle(dir, publicKey)
	if err == nil {
		t.Fatalf("the verifier read a directory as a bundle and reported %+v", report)
	}
	if !strings.Contains(err.Error(), "verify jsonl") {
		t.Fatalf("err = %v, want the rows open refused", err)
	}
}
