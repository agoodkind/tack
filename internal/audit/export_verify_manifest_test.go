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

// chainedExportTestRows builds n rows with valid prev_hash links on one shard.
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

// TestVerifyBundleReportsOversizedManifestCount checks that an untrusted count
// does not control allocation. The report must preserve the claimed count and
// reject its mismatch with the three actual rows (TACK-469).
func TestVerifyBundleReportsOversizedManifestCount(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 3)
	pub := writeSignedExportTestBundle(t, dir, rows)
	rewriteManifestRowCount(t, dir, math.MaxInt64)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("VerifyBundle must report on an invalid manifest count, not fail outright: %v", err)
	}

	if report.RowsScanned != 3 {
		t.Fatalf(
			"RowsScanned = %d, want the 3 rows in the file",
			report.RowsScanned,
		)
	}

	if report.ManifestRowCount != math.MaxInt64 {
		t.Fatalf(
			"ManifestRowCount = %d, want the claimed %d in the report",
			report.ManifestRowCount,
			math.MaxInt64,
		)
	}

	verdict := report.Err()
	if verdict == nil {
		t.Fatal("verification accepted a manifest count larger than the file's row count")
	}

	if !strings.Contains(verdict.Error(), "declares 9223372036854775807 rows") {
		t.Fatalf("verdict = %v, want it to name the claimed count", verdict)
	}
}

// TestVerifyBundleRejectsManifestRowCountMismatch isolates the count check
// with a valid signature, digest, and row hashes.
func TestVerifyBundleRejectsManifestRowCountMismatch(t *testing.T) {
	dir := t.TempDir()
	rows := chainedExportTestRows(t, 3)
	pub := writeSignedExportTestBundleDeclaring(t, dir, rows, len(rows)+1)

	report, err := VerifyBundle(dir, pub)
	if err != nil {
		t.Fatalf("VerifyBundle: %v", err)
	}

	if !report.SignatureOK || !report.FileSHA256OK || report.HashMatches != report.RowsScanned {
		t.Fatalf(
			"signature=%v digest=%v hashes=%d/%d: signature, digest, and hashes must pass before checking the count",
			report.SignatureOK,
			report.FileSHA256OK,
			report.HashMatches,
			report.RowsScanned,
		)
	}

	verdict := report.Err()
	if verdict == nil {
		t.Fatal("verification accepted a signed manifest with an incorrect row count")
	}

	if !strings.Contains(verdict.Error(), "declares 4 rows but the events file contains 3") {
		t.Fatalf("verdict = %v, want the declared and actual counts named", verdict)
	}
}

// rewriteManifestRowCount changes the count without updating the signature.
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
