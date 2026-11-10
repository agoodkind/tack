package audit

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// chainedExportTestRows builds n rows on one shard whose prev_hash links are
// intact, so a verifier failure on them can only come from the manifest.
func chainedExportTestRows(t *testing.T, n int) []Row {
	t.Helper()
	now := time.Now().UTC()
	orgID := uuid.Must(uuid.NewV7())
	rows := make([]Row, 0, n)
	var previous []byte
	for i := range n {
		row := Row{
			OrgID:       orgID,
			EventTime:   now.Add(time.Duration(i) * time.Second),
			EventID:     uuid.Must(uuid.NewV7()),
			Seq:         int64(i + 1),
			Shard:       1,
			ActorID:     uuid.Must(uuid.NewV7()),
			ActorKind:   1,
			Action:      "node.read",
			Outcome:     OutcomeOK,
			EntityID:    uuid.Must(uuid.NewV7()),
			Context:     EventContext{OrgID: orgID, RequestID: "req-count", TraceID: "trace-count", Source: SourceMCP},
			PrevHash:    previous,
			HashVersion: auditHashVersion3,
		}
		row.RowHash = hashExportTestRow(t, row)
		previous = row.RowHash
		rows = append(rows, row)
	}
	return rows
}

// TestVerifyBundleSurvivesAManifestClaimingMoreRowsThanCanExist is TACK-469's
// core case. The verifier sized its link slice from the manifest's row count
// before anything about the manifest was trusted, so a bundle claiming an
// absurd count made it allocate for the claim on the line before it would have
// rejected the bundle. Offline inspection of a foreign bundle is exactly what
// VerifyBundle exists for, so a hostile manifest is its normal input.
func TestVerifyBundleSurvivesAManifestClaimingMoreRowsThanCanExist(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 3)
	pub := writeSignedExportTestBundle(t, dir, rows)
	rewriteManifestRowCount(t, dir, math.MaxInt64)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("VerifyBundle must report on a lying manifest, not fail outright: %v", err)
	}
	if report.RowsScanned != 3 {
		t.Fatalf("RowsScanned = %d, want 3: the file, not the manifest, decides what was scanned", report.RowsScanned)
	}
	if report.ManifestRowCount != math.MaxInt64 {
		t.Fatalf("ManifestRowCount = %d, want the claimed %d carried into the report", report.ManifestRowCount, math.MaxInt64)
	}
	verdict := report.Err()
	if verdict == nil {
		t.Fatal("a manifest claiming more rows than the file holds must fail verification")
	}
	if !strings.Contains(verdict.Error(), "declares 9223372036854775807 rows") {
		t.Fatalf("verdict = %v, want it to name the claimed count", verdict)
	}
}

// TestVerifyBundleRejectsAManifestWhoseCountDisagreesWithItsFile pins the
// count check on its own. The manifest here is validly signed and its digest
// matches, which is the shape a truncating exporter produces when it signs
// whatever count it stopped at; only the row count is wrong, and that alone
// has to fail the bundle.
func TestVerifyBundleRejectsAManifestWhoseCountDisagreesWithItsFile(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 3)
	pub := writeSignedExportTestBundleDeclaring(t, dir, rows, len(rows)+1)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}
	if !report.SignatureOK || !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
		t.Fatalf("signature=%v digest=%v hashes=%d/%d: every other check must pass so the count check is the one under test",
			report.SignatureOK, report.FileSHA256OK, report.HashMatches, report.RowsScanned)
	}
	verdict := report.Err()
	if verdict == nil {
		t.Fatal("a signed manifest whose row count disagrees with its own file must fail verification")
	}
	if !strings.Contains(verdict.Error(), "declares 4 rows but the events file holds 3") {
		t.Fatalf("verdict = %v, want the declared and actual counts named", verdict)
	}
}

// rewriteManifestRowCount edits the row count in place, leaving the signature
// as it was, which is what a tampered or corrupt manifest looks like.
func rewriteManifestRowCount(t *testing.T, dir string, rowCount int) {
	t.Helper()
	path := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ExportManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.RowCount = rowCount
	edited, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
}
