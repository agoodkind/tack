package ops

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

// TestCategoryFromName locks the artifact-name-to-category dispatch table
// so a future filename rename cannot silently route an artifact to the
// wrong checker.
func TestCategoryFromName(t *testing.T) {
	cases := []struct {
		name string
		want artifactCategory
	}{
		{"fdb-snapshot.tar.gz", categoryFDB},
		{"fdbbackup.tar.gz", categoryFDB},
		{"tack_fdb-data.tar.gz", categoryFDB},
		{"tack_fdb-cluster.tar.gz", categoryFDB},
		{"yugabyte.tar.gz", categoryYugabyteTar},
		{"audit-snapshot-20260509.tar.gz", categoryYugabyteTar},
		{"tack_yugabyte-data.tar.gz", categoryYugabyteTar},
		{"yugabyte.sql", categoryPgDumpYugabyte},
		{"audit.sql.gz", categoryPgDumpYugabyte},
		{"temporal-db.sql", categoryPgDumpTemporal},
		{"temporal.sql.gz", categoryPgDumpTemporal},
		{"tack_meili-data.tar.gz", categoryMeilisearch},
		{"meilisearch.tar.gz", categoryMeilisearch},
		{"meili.tar.gz", categoryMeilisearch},
		{"random.txt", categoryUnknown},
		{"MANIFEST.txt", categoryUnknown},
	}
	for _, tc := range cases {
		got := categoryFromName(tc.name)
		if got != tc.want {
			t.Errorf("categoryFromName(%q) = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestCheckPgDumpEmpty asserts the empty-file branch of the pg_dump
// verifier. The 2026-04-25 silent-empty-backup defect was an empty FDB
// archive; the same shape check has to hold for SQL dumps because the
// 2026-05-09 fixes added Yugabyte and Temporal-DB to the coverage set.
func TestCheckPgDumpEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yugabyte.sql")
	err := os.WriteFile(path, []byte(""), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	err = checkPgDump(path)
	if err == nil {
		t.Fatalf("expected error on empty pg_dump, got nil")
	}
}

// TestCheckPgDumpHappyPath asserts a dump containing CREATE TABLE passes.
func TestCheckPgDumpHappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yugabyte.sql")
	err := os.WriteFile(path, []byte("-- header\nCREATE TABLE foo (id int);\n"), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	err = checkPgDump(path)
	if err != nil {
		t.Errorf("expected pass on real-looking dump, got %v", err)
	}
}

// TestCheckFDBArchiveDetectsEmpty exercises the exact silent-empty-backup
// signature the plan calls out as non-negotiable.
func TestCheckFDBArchiveDetectsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fdb-snapshot.tar.gz")
	mustWriteTarGz(t, path, nil)
	err := checkFDBArchive(path)
	if err == nil {
		t.Fatalf("expected empty-archive failure, got nil")
	}
}

// TestCheckFDBArchiveAcceptsBackupShape confirms a structurally valid
// fdbbackup tarball passes. Marker files match what checkFDBArchive requires.
func TestCheckFDBArchiveAcceptsBackupShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fdb-snapshot.tar.gz")
	mustWriteTarGz(t, path, []tarMember{
		{name: "backup-2026-05-09/snapshots/snapshot,123,456", body: nil},
		{name: "backup-2026-05-09/logs/log-1", body: []byte("x")},
		{name: "backup-2026-05-09/kvranges/kv-1", body: []byte("x")},
		{name: "backup-2026-05-09/properties/log_begin_version", body: []byte("0")},
	})
	err := checkFDBArchive(path)
	if err != nil {
		t.Errorf("expected pass on well-formed fdbbackup shape, got %v", err)
	}
}

// TestVerifyManifestHashesDetectsMismatch confirms tampering with a file
// after the manifest is written produces a hash failure at verify time.
func TestVerifyManifestHashesDetectsMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yugabyte.sql")
	err := os.WriteFile(path, []byte("CREATE TABLE foo (id int);"), 0o600)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	manifest, err := writeManifest(ctx, log, dir)
	if err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	// Mutate the file body without updating the manifest.
	err = os.WriteFile(path, []byte("CREATE TABLE other (id int);"), 0o600)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	fails, err := verifyManifestHashes(ctx, log, dir, manifest)
	if err != nil {
		t.Fatalf("verifyManifestHashes err: %v", err)
	}
	if len(fails) != 1 {
		t.Fatalf("expected 1 hash failure, got %d (%v)", len(fails), fails)
	}
}

type tarMember struct {
	name string
	body []byte
}

func mustWriteTarGz(t *testing.T, path string, members []tarMember) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for _, m := range members {
		err := tw.WriteHeader(&tar.Header{
			Name: m.name,
			Mode: 0o600,
			Size: int64(len(m.body)),
		})
		if err != nil {
			t.Fatalf("tar header: %v", err)
		}
		_, err = tw.Write(m.body)
		if err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
}
