// export_bundle_helpers_test.go provides shared bundle inspection and fixture helpers.

package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// bundleRowsFileNames returns per-export rows file names in dir.
func bundleRowsFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundle dir: %v", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, exportEventsPrefix) &&
			strings.HasSuffix(name, exportEventsSuffix) {
			names = append(names, name)
		}
	}

	return names
}

// publishedBundle reads the published manifest and its referenced rows file.
func publishedBundle(t *testing.T, dir string) (ExportManifest, []byte) {
	t.Helper()
	manifest, err := readExportManifest(dir)
	if err != nil {
		t.Fatalf("read published manifest: %v", err)
	}

	name, err := bundleEventsFileName(manifest)
	if err != nil {
		t.Fatalf("resolve the manifest's events file: %v", err)
	}

	rows, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read the rows the manifest names: %v", err)
	}

	return manifest, rows
}

// assertDirectoryHoldsOnlyItsBundle rejects staged, abandoned, or superseded
// files. Its explicit allowlist is independent of the production reclaim predicate.
func assertDirectoryHoldsOnlyItsBundle(t *testing.T, dir string) {
	t.Helper()
	allowed := map[string]bool{exportManifestFile: true, exportActivityLockFile: true}
	if manifest, err := readExportManifest(dir); err == nil {
		name, nameErr := bundleEventsFileName(manifest)
		if nameErr != nil {
			t.Fatalf("resolve the manifest's events file: %v", nameErr)
		}

		allowed[name] = true
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundle dir: %v", err)
	}

	for _, entry := range entries {
		if !allowed[entry.Name()] {
			t.Fatalf(
				"the bundle directory contains %s outside the published bundle",
				entry.Name(),
			)
		}
	}
}

// exportTestRows generates the requested number of correctly chained rows.
func exportTestRows(t *testing.T, orgID uuid.UUID, rows int, observe func(int)) *ledgerRowStream {
	t.Helper()

	return &ledgerRowStream{
		t:       t,
		orgID:   orgID,
		rows:    rows,
		shards:  4,
		base:    time.Now().UTC().Add(-time.Duration(rows) * time.Second),
		observe: observe,
	}
}

// copyBundleRows copies a rows file under the file name in its manifest.
func copyBundleRows(t *testing.T, fromDir, toDir, name string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(fromDir, name))
	if err != nil {
		t.Fatalf("read rows %s: %v", name, err)
	}

	if err := os.WriteFile(filepath.Join(toDir, name), body, 0o600); err != nil {
		t.Fatalf("place rows %s: %v", name, err)
	}
}

// rewriteManifestField changes or removes a manifest field without updating
// its signature. A nil value removes the key.
func rewriteManifestField(t *testing.T, dir, key string, value *string) {
	t.Helper()
	path := filepath.Join(dir, exportManifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var loose map[string]json.RawMessage
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatal(err)
	}

	if value == nil {
		delete(loose, key)
	} else {
		encoded, marshalErr := json.Marshal(*value)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}

		loose[key] = encoded
	}

	edited, err := json.Marshal(loose)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
}
