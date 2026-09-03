package ops

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// testArchiveEntry is one member of a fixture archive, written the way the
// export's tar writes them: names prefixed "./", a directory entry ahead of the
// files under it.
type testArchiveEntry struct {
	name     string
	typeflag byte
	body     string
}

// writeTestArchive builds a gzip tar of the given members and returns its path.
// The members are written by hand rather than by tarring a tree, so a test can
// put shapes in an archive that no local filesystem would produce.
func writeTestArchive(t *testing.T, entries []testArchiveEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ybNodeArchiveObject)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create fixture archive: %v", err)
	}
	defer file.Close()
	zipped := gzip.NewWriter(file)
	archive := tar.NewWriter(zipped)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Mode:     0o600,
			Size:     int64(len(entry.body)),
			Typeflag: entry.typeflag,
		}
		if entry.typeflag == tar.TypeSymlink {
			header.Size = 0
			header.Linkname = entry.body
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("write header %s: %v", entry.name, err)
		}
		if entry.typeflag == tar.TypeReg {
			if _, err := archive.Write([]byte(entry.body)); err != nil {
				t.Fatalf("write body %s: %v", entry.name, err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("close fixture tar: %v", err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatalf("close fixture gzip: %v", err)
	}
	return path
}

// tabletArchiveEntries is one tablet's directory entry and its files, the shape
// the export's `find -type d | tar` produces.
func tabletArchiveEntries(dir string, files map[string]string) []testArchiveEntry {
	entries := []testArchiveEntry{{name: "./" + dir + "/", typeflag: tar.TypeDir, body: ""}}
	for _, name := range []string{"CURRENT", "000123.sst", "MANIFEST-000004"} {
		if body, found := files[name]; found {
			entries = append(entries, testArchiveEntry{
				name: "./" + dir + "/" + name, typeflag: tar.TypeReg, body: body,
			})
		}
	}
	return entries
}
