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

// chainedExportTestRows builds n rows on one shard with intact prev_hash
// links. The fixture isolates manifest failures from chain failures.
func chainedExportTestRows(t *testing.T, n int) []Row {
	t.Helper()
	now := time.Now().UTC()
	orgID := uuid.Must(uuid.NewV7())
	rows := make([]Row, 0, n)
	var previous []byte

	for i := range n {
		row := Row{
			OrgID:     orgID,
			EventTime: now.Add(time.Duration(i) * time.Second),
			EventID:   uuid.Must(uuid.NewV7()),
			Seq:       int64(i + 1),
			Shard:     1,
			ActorID:   uuid.Must(uuid.NewV7()),
			ActorKind: 1,
			Action:    "node.read",
			Outcome:   OutcomeOK,
			EntityID:  uuid.Must(uuid.NewV7()),
			Context: EventContext{
				OrgID:     orgID,
				RequestID: "req-count",
				TraceID:   "trace-count",
				Source:    SourceMCP,
			},
			PrevHash:    previous,
			HashVersion: auditHashVersion3,
		}
		row.RowHash = hashExportTestRow(t, row)
		previous = row.RowHash
		rows = append(rows, row)
	}

	return rows
}

// TestVerifyBundleSurvivesAManifestClaimingMoreRowsThanCanExist covers the
// core TACK-469 regression. The old verifier allocated its link slice from
// the untrusted manifest row count before rejecting the bundle. A huge count
// could trigger excessive allocation. VerifyBundle supports offline
// inspection of foreign bundles and must handle hostile manifests.
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
		t.Fatalf(
			"RowsScanned = %d, want 3: the file, not the manifest, decides what was scanned",
			report.RowsScanned,
		)
	}

	if report.ManifestRowCount != math.MaxInt64 {
		t.Fatalf(
			"ManifestRowCount = %d, want the claimed %d carried into the report",
			report.ManifestRowCount,
			math.MaxInt64,
		)
	}

	verdict := report.Err()
	if verdict == nil {
		t.Fatal("a manifest claiming more rows than the file holds must fail verification")
	}

	if !strings.Contains(verdict.Error(), "declares 9223372036854775807 rows") {
		t.Fatalf("verdict = %v, want it to name the claimed count", verdict)
	}
}

// TestVerifyBundleRejectsAManifestWhoseCountDisagreesWithItsFile isolates
// the row-count check. The manifest has a valid signature and matching file
// digest and row hashes. A truncating exporter could sign a count that does
// not match its file. That mismatch alone must fail verification.
func TestVerifyBundleRejectsAManifestWhoseCountDisagreesWithItsFile(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 3)
	pub := writeSignedExportTestBundleDeclaring(t, dir, rows, len(rows)+1)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	if !report.SignatureOK || !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
		t.Fatalf(
			"signature=%v digest=%v hashes=%d/%d: every other check must pass so the count check is the one under test",
			report.SignatureOK,
			report.FileSHA256OK,
			report.HashMatches,
			report.RowsScanned,
		)
	}

	verdict := report.Err()
	if verdict == nil {
		t.Fatal(
			"a signed manifest whose row count disagrees with its own file must fail verification",
		)
	}

	if !strings.Contains(verdict.Error(), "declares 4 rows but the events file holds 3") {
		t.Fatalf("verdict = %v, want the declared and actual counts named", verdict)
	}
}

// rewriteManifestRowCount changes the count and preserves the original
// signature to represent a tampered or corrupt manifest.
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
