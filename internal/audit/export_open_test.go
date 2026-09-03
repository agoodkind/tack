// export_open_test.go covers what the verifier is willing to open. The rows
// file is named by the manifest, and the manifest is unverified until the rows
// have been read, so an entry planted in the bundle directory under that name
// is read before any verdict exists.

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

// exportedTestBundle writes a real bundle and returns its directory, the key
// that verifies it, and the path of the rows file its manifest names.
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

// TestTheVerifierRefusesRowsThatAreASymlink pins that the bundle's rows are
// read from the bundle. The manifest supplies the name, and a name check can
// only keep that name inside the directory; it cannot say what the entry under
// it is. A symbolic link planted there sends every byte the verifier digests and
// hashes to a file outside the bundle, chosen by whoever planted the link, and
// the verdict is still reported as a verdict about the bundle.
//
// The link points at a byte-for-byte copy of the real rows on purpose. The
// digest and the signature both pass on that content, so a verifier that follows
// the link returns a clean report, and only refusing the link itself catches it.
// That is what makes content checks the wrong place to fix this.
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

// TestTheVerifierRefusesRowsThatAreANamedPipe pins that the verifier always
// reaches a verdict. Opening a pipe that has no writer blocks in open(2) with no
// timeout, so a pipe planted under the name the manifest carries stops the
// verifier before it has checked anything, and it never returns to report that
// something was wrong.
//
// The wait is bounded here so a regression fails this test rather than hanging
// the suite.
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

// TestTheVerifierRefusesRowsThatAreADirectory pins the third entry an attacker
// can leave under the manifest's name without any privilege. It reads as an
// empty file on some systems and fails the read on others, and neither is a
// verdict about a bundle.
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
