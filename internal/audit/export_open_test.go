// export_open_test.go checks filesystem type rejection before reading untrusted
// bundle rows for signature verification.

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

// exportedTestBundle writes a bundle and returns its directory, public key,
// and manifest-selected rows path.
func exportedTestBundle(t *testing.T) (string, ed25519.PublicKey, string) {
	t.Helper()
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	manifest, err := Export(
		context.Background(),
		exportTestRows(t, orgID, 12, nil),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	rowsPath := filepath.Join(dir, manifest.EventsFile)
	if _, err := os.Stat(rowsPath); err != nil {
		t.Fatalf("the export did not leave the rows its manifest names: %v", err)
	}

	return dir, publicKey, rowsPath
}

// TestVerifyBundleRejectsSymlinkRows requires rejection of the file type even
// when the external target contains identical rows with a valid digest.
func TestVerifyBundleRejectsSymlinkRows(t *testing.T) {
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

// TestVerifyBundleRejectsFIFORowsWithoutBlocking checks rejection of a FIFO
// (first-in, first-out) special file without waiting indefinitely for a writer.
func TestVerifyBundleRejectsFIFORowsWithoutBlocking(t *testing.T) {
	dir, publicKey, rowsPath := exportedTestBundle(t)

	if err := os.Remove(rowsPath); err != nil {
		t.Fatalf("remove the published rows: %v", err)
	}

	if err := syscall.Mkfifo(rowsPath, 0o600); err != nil {
		t.Skipf("this filesystem cannot create a FIFO special file: %v", err)
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
			t.Fatalf("the verifier accepted a FIFO special file as bundle rows: %+v", got.report)
		}

		if !strings.Contains(got.err.Error(), "verify jsonl") {
			t.Fatalf("err = %v, want the rows open refused", got.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the verifier did not reject the FIFO special file within 10s")
	}
}

// TestVerifyBundleRejectsDirectoryRows checks that a directory cannot substitute
// for the manifest-selected regular file.
func TestVerifyBundleRejectsDirectoryRows(t *testing.T) {
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
