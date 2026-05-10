package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteAndReadManifestRoundTrip exercises the verification gate the
// plan calls out: the SHA-256 written into MANIFEST.txt must match the
// SHA-256 of the same file when re-read from disk. The test fixture is
// three small files plus an excluded MANIFEST.txt; round-trip reads must
// reproduce the same entry set.
func TestWriteAndReadManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"alpha.sql":         []byte("CREATE TABLE alpha (id int);"),
		"beta.tar.gz":       []byte("not really a tarball"),
		"sub/gamma.sql.gz":  []byte("not really gzipped sql"),
		"empty-but-tracked": []byte(""),
	}
	for rel, body := range files {
		full := filepath.Join(dir, rel)
		err := os.MkdirAll(filepath.Dir(full), 0o750)
		if err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		err = os.WriteFile(full, body, 0o600)
		if err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	written, err := writeManifest(ctx, log, dir)
	if err != nil {
		t.Fatalf("writeManifest: %v", err)
	}
	if len(written) != len(files) {
		t.Fatalf("expected %d entries written, got %d", len(files), len(written))
	}

	read, err := readManifest(ctx, log, dir)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}
	if len(read) != len(written) {
		t.Fatalf("read %d entries, wrote %d", len(read), len(written))
	}
	for i, w := range written {
		if read[i].SHA256 != w.SHA256 {
			t.Errorf("entry %d sha mismatch: read=%s wrote=%s", i, read[i].SHA256, w.SHA256)
		}
		if read[i].Size != w.Size {
			t.Errorf("entry %d size mismatch: read=%d wrote=%d", i, read[i].Size, w.Size)
		}
		if read[i].RelPath != w.RelPath {
			t.Errorf("entry %d rel mismatch: read=%s wrote=%s", i, read[i].RelPath, w.RelPath)
		}
	}

	// The manifest itself must be excluded from its own listing; otherwise
	// the digest chases its own tail. This is the exact 2026-04-25 silent-
	// empty-backup precondition the gate exists to prevent at the verify
	// layer.
	for _, e := range read {
		if e.RelPath == manifestFileName {
			t.Errorf("manifest entry should be excluded from MANIFEST.txt; saw %s", e.RelPath)
		}
	}

	// Independently re-hash one file and confirm the manifest agrees. This
	// is the round-trip equality the plan verification gate calls for.
	body := files["alpha.sql"]
	h := sha256.Sum256(body)
	want := hex.EncodeToString(h[:])
	found := false
	for _, e := range read {
		if e.RelPath == "alpha.sql" {
			found = true
			if e.SHA256 != want {
				t.Errorf("alpha.sql sha mismatch: manifest=%s independent=%s", e.SHA256, want)
			}
			if e.Size != int64(len(body)) {
				t.Errorf("alpha.sql size mismatch: manifest=%d body=%d", e.Size, len(body))
			}
		}
	}
	if !found {
		t.Errorf("alpha.sql not present in manifest read")
	}
}

// TestReadManifestRejectsMalformedLines locks down the malformed-line
// branch so a corrupt manifest cannot silently pass.
func TestReadManifestRejectsMalformedLines(t *testing.T) {
	dir := t.TempDir()
	bad := "twofields only"
	err := os.WriteFile(filepath.Join(dir, manifestFileName), []byte(bad+"\n"), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	_, err = readManifest(ctx, log, dir)
	if err == nil {
		t.Fatalf("expected error on malformed manifest, got nil")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error should mention malformed; got %v", err)
	}
}
