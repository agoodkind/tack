// export_reclaim_scope_test.go checks removal of unreferenced exporter files
// and preservation of operator files beside a bundle.

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

// TestAnExportReclaimsTheRowsNoManifestNames checks removal of interrupted
// export files and superseded rows after publication. Reclamation uses manifest
// references and exporter ownership rather than file age.
func TestAnExportReclaimsTheRowsNoManifestNames(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Include current rows, earlier staged rows, and staged manifests left by interruption.
	abandoned := []string{
		exportEventsFileName(uuid.Must(uuid.NewV7())),
		legacyExportEventsFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
		exportManifestFile + "." + uuid.Must(uuid.NewV7()).String() + stagedSuffix,
	}
	for _, name := range abandoned {
		if err := os.WriteFile(
			filepath.Join(dir, name),
			[]byte("half a ledger\n"),
			0o600,
		); err != nil {
			t.Fatalf("plant %s: %v", name, err)
		}
	}

	firstManifest, err := Export(
		context.Background(),
		exportTestRows(t, orgID, 40, nil),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
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

	// Publication of the second manifest makes the first export's rows reclaimable.
	secondManifest, err := Export(
		context.Background(),
		exportTestRows(t, orgID, 25, nil),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
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

// TestTheReclaimSparesFilesThisExporterDidNotWrite checks canonical export IDs
// rather than prefix and suffix matches alone. Operator files must survive;
// genuine unreferenced exporter files must disappear to verify reclamation.
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

	manifest, err := Export(
		context.Background(),
		exportTestRows(t, orgID, 30, nil),
		privateKey,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	)
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
