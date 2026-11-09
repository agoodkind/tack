// These tests check that export cleanup removes unselected exporter-owned files
// and preserves files that an operator placed beside the bundle.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestAnExportReclaimsTheRowsNoManifestNames checks cleanup of abandoned and
// superseded rows. A process killed during export can leave a rows file. Each
// export uses a distinct filename, and later writes do not overwrite that
// abandoned file. Without cleanup, every interruption can add another
// ledger-sized file.
//
// Cleanup selects files by exporter ownership and the current manifest, without
// an age cutoff. The same pass removes the previous bundle's superseded rows.
func TestAnExportReclaimsTheRowsNoManifestNames(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// An interrupted export can leave an export-specific rows file or a staged
	// manifest. Earlier revisions also wrote a staged rows filename.
	abandoned := []string{
		exportEventsFileName(uuid.Must(uuid.NewV7())),
		legacyExportEventsFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
		exportManifestFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
	}
	for _, name := range abandoned {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("half a ledger\n"), 0o600); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}

	firstManifest, err := Export(context.Background(), exportTestRows(t, orgID, 40, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	for _, name := range abandoned {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
			t.Fatalf("the export left the abandoned %s in place", name)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, firstManifest.EventsFile)); statErr != nil {
		t.Fatalf("the export reclaimed the rows its own manifest names: %v", statErr)
	}

	// The second export replaces the first manifest. Cleanup must remove the first
	// export's rows after the manifest stops selecting them.
	secondManifest, err := Export(context.Background(), exportTestRows(t, orgID, 25, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, firstManifest.EventsFile)); statErr == nil {
		t.Fatal("the superseded rows of the previous bundle were kept")
	}
	if _, statErr := os.Stat(filepath.Join(dir, secondManifest.EventsFile)); statErr != nil {
		t.Fatalf("the published rows were reclaimed: %v", statErr)
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the bundle left after two reclaims does not verify: %v", verdict)
	}
	assertDirectoryHoldsOnlyItsBundle(t, dir)
}

// TestTheReclaimSparesFilesThisExporterDidNotWrite checks that cleanup
// recognizes the exact filenames the exporter produces. Operators also use
// bundle directories, and cleanup deletes eligible files without confirmation.
//
// An earlier predicate accepted any text between a prefix and suffix. It
// deleted every filename in the operator group, including auditor files that
// only resembled export output. The exporter inserts its export ID using one
// canonical spelling; the operator examples do not match that output.
//
// The exporter group checks that cleanup still deletes genuine export files. A
// predicate that never selected a file would otherwise pass the preservation
// assertions.
func TestTheReclaimSparesFilesThisExporterDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	realExportID := uuid.Must(uuid.NewV7()).String()
	operatorFiles := []string{
		"events-before-the-incident.jsonl",
		"events-2026-05-01.jsonl",
		"events-.jsonl",
		"events-" + strings.ToUpper(realExportID) + ".jsonl",
		"events-{" + realExportID + "}.jsonl",
		"events-urn:uuid:" + realExportID + ".jsonl",
		exportManifestFile + ".auditor-notes" + stagedSuffix,
		legacyExportEventsFile + ".backup" + stagedSuffix,
	}
	exporterFiles := []string{
		exportEventsFileName(uuid.Must(uuid.NewV7())),
		legacyExportEventsFile,
		legacyExportEventsFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
		exportManifestFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
	}
	for _, name := range append(append([]string{}, operatorFiles...), exporterFiles...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("planted\n"), 0o600); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}

	manifest, err := Export(context.Background(), exportTestRows(t, orgID, 30, nil),
		privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}

	for _, name := range operatorFiles {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
			t.Errorf("the reclaim removed %s, which no export wrote: %v", name, statErr)
		}
	}
	for _, name := range exporterFiles {
		if _, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil {
			t.Errorf("the reclaim kept %s, which an export wrote and no manifest names", name)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, manifest.EventsFile)); statErr != nil {
		t.Fatalf("the reclaim removed the rows this bundle's manifest names: %v", statErr)
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("the published bundle does not verify: %v", verdict)
	}
}
