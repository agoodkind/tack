package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestVerifyBundleRejectsARowFromAnotherOrg checks org membership independently
// of valid row hashes and chain links.
func TestVerifyBundleRejectsARowFromAnotherOrg(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 3)
	foreign := uuid.Must(uuid.NewV7())
	rows[1].OrgID = foreign
	rows[1].Context.OrgID = foreign
	rows[1].RowHash = hashExportTestRow(t, rows[1])
	rows[2].PrevHash = rows[1].RowHash
	rows[2].RowHash = hashExportTestRow(t, rows[2])
	pub := writeSignedExportTestBundle(t, dir, rows)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	if report.HashMatches != 3 {
		t.Fatalf(
			"HashMatches = %d, want 3 valid hashes before testing org membership",
			report.HashMatches,
		)
	}

	verdict := report.Err()
	if verdict == nil || !strings.Contains(verdict.Error(), "belongs to org") {
		t.Fatalf("verdict = %v, want a failure naming the foreign org", verdict)
	}
}

// TestVerifyBundleRejectsARepeatedSequence appends the duplicate last to isolate
// sequence rejection from a later row's predecessor mismatch.
func TestVerifyBundleRejectsARepeatedSequence(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 3)
	replay := rows[2]
	replay.EventID = uuid.Must(uuid.NewV7())
	replay.RowHash = hashExportTestRow(t, replay)
	rows = append(rows, replay)
	pub := writeSignedExportTestBundle(t, dir, rows)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	if report.HashMatches != 4 {
		t.Fatalf(
			"HashMatches = %d, want 4 valid hashes before testing sequence rejection",
			report.HashMatches,
		)
	}

	verdict := report.Err()
	if verdict == nil || !strings.Contains(verdict.Error(), "repeats sequence 3") {
		t.Fatalf("verdict = %v, want a break naming the repeated sequence", verdict)
	}
}

// TestVerifyBundleRefusesAnOversizedManifest pads valid JSON with whitespace
// to isolate the manifest size limit.
func TestVerifyBundleRefusesAnOversizedManifest(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 2)
	pub := writeSignedExportTestBundle(t, dir, rows)
	path := filepath.Join(dir, "manifest.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	padded := append([]byte(strings.Repeat(" ", maxManifestBytes+1)), original...)
	if err := os.WriteFile(path, padded, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = VerifyBundle(dir, pub)

	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want the manifest refused for its size before it is parsed", err)
	}
}

// TestVerifyBundleRefusesAManifestWithAPlantedField requires unknown-key
// rejection instead of silently discarding unsigned fields during decoding.
func TestVerifyBundleRefusesAManifestWithAPlantedField(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 2)
	pub := writeSignedExportTestBundle(t, dir, rows)
	path := filepath.Join(dir, "manifest.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var loose map[string]json.RawMessage
	if err := json.Unmarshal(original, &loose); err != nil {
		t.Fatal(err)
	}

	loose["auditor_note"] = json.RawMessage(`"rows verified by hand"`)
	planted, err := json.Marshal(loose)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, planted, 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = VerifyBundle(dir, pub)

	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("err = %v, want the planted field refused", err)
	}
}

// TestVerifyBundleReportsInvalidPublicKeyLength checks that an invalid key length
// fails signature verification without panicking.
func TestVerifyBundleReportsInvalidPublicKeyLength(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 2)
	pub := writeSignedExportTestBundle(t, dir, rows)

	report, err := VerifyBundle(dir, pub[:len(pub)-1])
	if err != nil {
		t.Fatalf("VerifyBundle must report on a malformed key, not fail outright: %v", err)
	}

	if report.SignatureOK {
		t.Fatal("a key of the wrong length must not verify anything")
	}
}
