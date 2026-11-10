// export_bundle_helpers_test.go is how the export tests read, populate, and
// assert on a bundle directory. Every test that drives Export into a directory
// and then looks at what it left shares these.

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

// bundleRowsFileNames returns the names of the per-export rows files sitting in
// a bundle directory, whatever export wrote them.
func bundleRowsFileNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundle dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, exportEventsPrefix) && strings.HasSuffix(name, exportEventsSuffix) {
			names = append(names, name)
		}
	}
	return names
}

// publishedBundle reads the manifest a bundle directory publishes and the bytes
// of the rows file it names, which is the only pair that is the bundle.
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

// assertDirectoryHoldsOnlyItsBundle fails when a bundle directory carries a
// file beyond the bundle itself: a staged file a failed export left, or the
// rows of an export that was superseded or died. The allowed names are listed
// here rather than read from the production predicate, so a change that
// widened what counts as reclaimable could not also widen this check.
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
			t.Fatalf("the bundle directory still holds %s, which is not part of the published bundle",
				entry.Name())
		}
	}
}

// exportTestRows streams a correctly chained ledger of the requested size.
func exportTestRows(t *testing.T, orgID uuid.UUID, rows int, observe func(int)) *ledgerRowStream {
	t.Helper()
	return &ledgerRowStream{
		t: t, orgID: orgID, rows: rows, shards: 4,
		base: time.Now().UTC().Add(-time.Duration(rows) * time.Second), observe: observe,
	}
}

// copyBundleRows places one export's rows file into another directory under the
// name its manifest carries.
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

// rewriteManifestField edits one key of the published manifest in place and
// leaves the signature as it was, which is what a tampered manifest looks like.
// A nil value removes the key.
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
