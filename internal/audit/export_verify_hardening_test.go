package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// TestVerifyBundleRejectsARowFromAnotherOrg pins that an export is one org's
// ledger. A row lifted intact from another org's export carries a hash that
// is valid for that org, so it verifies row by row and its link holds; only
// comparing the row's org to the manifest's catches it.
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
		t.Fatalf("HashMatches = %d, want 3: the foreign row is internally valid, so only the org check can catch it", report.HashMatches)
	}
	verdict := report.Err()
	if verdict == nil || !strings.Contains(verdict.Error(), "belongs to org") {
		t.Fatalf("verdict = %v, want a failure naming the foreign org", verdict)
	}
}

// TestVerifyBundleRejectsARepeatedSequence pins that a sequence which does not
// advance is a break, not a gap. The duplicate is the last row on purpose: a
// duplicate in the middle also breaks the next row's previous-hash link, which
// would let a gap-tolerant walk fail for the wrong reason and hide this check.
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
		t.Fatalf("HashMatches = %d, want 4: the replayed row is self-consistent so only the sequence check can catch it", report.HashMatches)
	}
	verdict := report.Err()
	if verdict == nil || !strings.Contains(verdict.Error(), "repeats sequence 3") {
		t.Fatalf("verdict = %v, want a break naming the repeated sequence", verdict)
	}
}

// TestVerifyBundleRefusesAnOversizedManifest pins the read bound. The padding
// is whitespace so the document stays valid JSON with no unknown field; only
// its size is wrong.
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

// TestVerifyBundleRefusesAManifestWithAPlantedField pins that a manifest key
// the type does not declare fails the parse. The signature covers only the
// declared fields, so a dropped key would be a planted field that verifies.
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

// TestVerifyBundleSurvivesAMalformedPublicKey pins that a key of the wrong
// length is a failed signature check, not a panic out of ed25519.Verify.
func TestVerifyBundleSurvivesAMalformedPublicKey(t *testing.T) {
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
