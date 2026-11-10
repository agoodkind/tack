// export_reclaim_scope_test.go covers which files a later export is allowed to
// free: every file this exporter wrote that no manifest names, and nothing an
// operator left beside the bundle.

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

// TestAnExportReclaimsTheRowsNoManifestNames pins the reclaim rule. A process
// killed mid-export leaves a rows file behind, and naming those files per
// export means no later run overwrites one, so without a reclaim they
// accumulate a ledger-sized file per interruption.
//
// The rule is exact rather than a guess about age: a bundle is a manifest and
// the rows it names, so every other file this exporter wrote is free. The
// superseded rows of the directory's previous bundle are covered by the same
// rule and are freed by the same pass.
func TestAnExportReclaimsTheRowsNoManifestNames(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Everything an interrupted export can leave: a rows file named for the
	// export that abandoned it, the staged rows name earlier revisions wrote,
	// and a staged manifest.
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

	// A second export supersedes the first, so the first's rows stop being named
	// and the same rule frees them.
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

// TestTheReclaimSparesFilesThisExporterDidNotWrite pins the limit of the
// reclaim rule. A bundle directory is somewhere an operator also works, and the
// reclaim unlinks without asking, so what counts as this exporter's file has to
// be the names it actually produces rather than the shape those names have.
//
// Every name in the operator group used to be reclaimed: the rule was a prefix
// and a suffix with anything at all between them, so a file an auditor left
// beside the bundle was deleted by the next export purely for being called
// something of that form. Each name here is one an export cannot have written,
// because this exporter puts the id of the export into every name it writes and
// renders it one way.
//
// The exporter group is the control. Without it a predicate that simply stopped
// reclaiming anything would pass this test while breaking the reclaim.
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
