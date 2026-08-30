package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

// exportPageSpanRows is larger than every page size the ledger reads by, so an
// export that silently inherited one would come back short. DefaultQueryPageLimit
// is 100 and MaxQueryPageLimit is 1000.
const exportPageSpanRows = 2500

// ledgerRowStream is a RowSource that manufactures a correctly chained ledger
// of the requested size, one row at a time, the way the database hands rows
// over. It keeps no row after handing it on, so what a test measures around
// Export is Export's own footprint and not the fixture's.
type ledgerRowStream struct {
	t      *testing.T
	orgID  uuid.UUID
	rows   int
	shards int
	base   time.Time
	// observe runs after each row reaches the export, so a test can sample
	// while the export is still running rather than after it has returned.
	observe func(index int)
}

// StreamQuery satisfies RowSource. Rows are emitted round-robin across shards,
// so each shard's sequence is contiguous and ascending and the bundle carries a
// chain the verifier can walk end to end.
func (s *ledgerRowStream) StreamQuery(_ context.Context, _ QueryFilter, visit RowVisitor) error {
	s.t.Helper()
	seqByShard := make([]int64, s.shards)
	prevByShard := make([][]byte, s.shards)
	for index := range s.rows {
		shard := index % s.shards
		seqByShard[shard]++
		row := Row{
			OrgID:     s.orgID,
			EventTime: s.base.Add(time.Duration(index) * time.Millisecond).Truncate(time.Microsecond),
			EventID:   uuid.Must(uuid.NewV7()),
			Seq:       seqByShard[shard],
			Shard:     int16(shard),
			ActorID:   uuid.Must(uuid.NewV7()),
			ActorKind: 1,
			Action:    "auth.token_used",
			Outcome:   OutcomeOK,
			// A realistic payload is the point: it is what makes holding the
			// rows expensive, and therefore what makes the budget below
			// separate a streaming export from a collecting one.
			Context: EventContext{
				OrgID:     s.orgID,
				RequestID: fmt.Sprintf("req-%d-export-stream-padding", index),
				TraceID:   fmt.Sprintf("trace-%d-export-stream-padding", index),
				Source:    SourceMCP,
			},
			PrevHash:    prevByShard[shard],
			HashVersion: auditHashVersion3,
		}
		row.RowHash = hashExportTestRow(s.t, row)
		prevByShard[shard] = row.RowHash
		if err := visit(row); err != nil {
			return err
		}
		if s.observe != nil {
			s.observe(index)
		}
	}
	return nil
}

// sliceRowSource hands over rows a test already built, in the order the slice
// holds them. It is how the ordering assertion drives the export with a row
// order the database is free to return.
type sliceRowSource struct {
	rows []Row
}

func (s *sliceRowSource) StreamQuery(_ context.Context, _ QueryFilter, visit RowVisitor) error {
	for _, row := range s.rows {
		if err := visit(row); err != nil {
			return err
		}
	}
	return nil
}

// exportTestFilter is the whole-range, no-limit filter a compliance export and
// the restore drill both use. A zero limit is the part under test: it must mean
// every row, not a page.
func exportTestFilter(orgID uuid.UUID) QueryFilter {
	return QueryFilter{
		OrgID:     orgID,
		Oldest:    time.Unix(0, 0).UTC(),
		Latest:    time.Date(9999, time.January, 1, 0, 0, 0, 0, time.UTC),
		Action:    "",
		ActorID:   uuid.Nil,
		EntityID:  uuid.Nil,
		RequestID: "",
		TraceID:   "",
		Limit:     0,
	}
}

// TestExportWritesEveryRowPastEveryPageSize is the defect this file exists for.
// The export used to read its rows through the page-sized query, which defaults
// a zero limit to one page, and to hold every row it read before writing any of
// them. Either half alone produces a bundle that reads as complete and is not:
// the manifest counts what was written, the digest covers what was written, and
// nothing in the bundle records what was left behind.
func TestExportWritesEveryRowPastEveryPageSize(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &ledgerRowStream{
		t: t, orgID: orgID, rows: exportPageSpanRows, shards: 8,
		base: time.Now().UTC().Add(-time.Duration(exportPageSpanRows) * time.Second), observe: nil,
	}

	manifest, err := Export(
		context.Background(), source, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if manifest.RowCount != exportPageSpanRows {
		t.Fatalf("manifest row count = %d, want %d; a zero limit must export every row, not a page",
			manifest.RowCount, exportPageSpanRows)
	}

	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.RowsScanned != exportPageSpanRows {
		t.Fatalf("rows scanned = %d, want %d", report.RowsScanned, exportPageSpanRows)
	}
	if report.HashMatches != exportPageSpanRows {
		t.Fatalf("hash matches = %d, want %d", report.HashMatches, exportPageSpanRows)
	}
	if report.ChainGapCount != 0 {
		t.Fatalf("sequence gaps = %d in a whole-ledger export, want 0", report.ChainGapCount)
	}
	// The manifest is signed over the row count and the digest, so this is what
	// proves both were accumulated from the rows actually written rather than
	// from a slice that was never built.
	if !report.FileSHA256OK || !report.SignatureOK {
		t.Fatalf("digest ok = %v, signature ok = %v, want both true",
			report.FileSHA256OK, report.SignatureOK)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("a complete export failed verification: %v", verdict)
	}
}

// TestExportRowOrderDoesNotChangeTheChainVerdict answers what streaming costs
// the chain check: nothing. A whole-range export carries no ORDER BY, so the
// database is free to return an org's rows in any order, including with a
// shard's rows split apart and its sequences descending. The verifier sorts
// what it scans into per-shard sequence order before it walks the chain, so the
// same rows in a different file order produce the same verdict: every row
// re-hashed, every link checked, no gap invented by the ordering.
func TestExportRowOrderDoesNotChangeTheChainVerdict(t *testing.T) {
	const shards, perShard = 6, 100
	orgID := uuid.Must(uuid.NewV7())
	chained := chainedLedgerRows(t, orgID, shards, perShard)

	inFileOrder := exportedVerifyReport(t, orgID, chained)
	scrambled := exportedVerifyReport(t, orgID, scrambleRowOrder(chained))

	for name, report := range map[string]*VerifyReport{"chain order": inFileOrder, "scrambled": scrambled} {
		if report.RowsScanned != shards*perShard {
			t.Fatalf("%s: rows scanned = %d, want %d", name, report.RowsScanned, shards*perShard)
		}
		if report.HashMatches != shards*perShard {
			t.Fatalf("%s: hash matches = %d, want %d", name, report.HashMatches, shards*perShard)
		}
		if len(report.ChainBreaks) != 0 {
			t.Fatalf("%s: chain breaks = %v, want none", name, report.ChainBreaks)
		}
		if report.ChainGapCount != 0 {
			t.Fatalf("%s: sequence gaps = %d, want 0; the row order must not invent a gap",
				name, report.ChainGapCount)
		}
		if verdict := report.Err(); verdict != nil {
			t.Fatalf("%s: %v", name, verdict)
		}
	}
}

// chainedLedgerRows builds perShard correctly linked rows for each shard.
func chainedLedgerRows(t *testing.T, orgID uuid.UUID, shards, perShard int) []Row {
	t.Helper()
	base := time.Now().UTC().Add(-time.Hour)
	rows := make([]Row, 0, shards*perShard)
	for shard := range shards {
		var prevHash []byte
		for seq := 1; seq <= perShard; seq++ {
			row := Row{
				OrgID:      orgID,
				EventTime:  base.Add(time.Duration(shard*perShard+seq) * time.Millisecond).Truncate(time.Microsecond),
				EventID:    uuid.Must(uuid.NewV7()),
				Seq:        int64(seq),
				Shard:      int16(shard),
				ActorID:    uuid.Must(uuid.NewV7()),
				ActorKind:  5,
				Action:     "ops.test.export_order",
				Outcome:    OutcomeOK,
				EntityKind: "system", EntityID: orgID,
				Context:     EventContext{OrgID: orgID, Source: SourceSystem},
				PrevHash:    prevHash,
				HashVersion: auditHashVersion3,
			}
			row.RowHash = hashExportTestRow(t, row)
			prevHash = row.RowHash
			rows = append(rows, row)
		}
	}
	return rows
}

// scrambleRowOrder returns the same rows in an order no chain walk could use
// directly: shards interleaved and each shard's sequence running backwards.
// This is the worst order a database with no ORDER BY could hand back.
func scrambleRowOrder(rows []Row) []Row {
	scrambled := make([]Row, len(rows))
	for i, row := range rows {
		scrambled[len(rows)-1-i] = row
	}
	for i := 0; i+1 < len(scrambled); i += 2 {
		scrambled[i], scrambled[i+1] = scrambled[i+1], scrambled[i]
	}
	return scrambled
}

// exportedVerifyReport exports the rows in the order given and returns the
// verifier's report on the bundle, so the assertion is on what the production
// verifier says about a real bundle rather than on the fixture.
func exportedVerifyReport(t *testing.T, orgID uuid.UUID, rows []Row) *VerifyReport {
	t.Helper()
	dir := t.TempDir()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	manifest, err := Export(
		context.Background(), &sliceRowSource{rows: rows}, privateKey, "ed25519:test",
		exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if manifest.RowCount != len(rows) {
		t.Fatalf("manifest row count = %d, want %d", manifest.RowCount, len(rows))
	}
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	return report
}

// TestExportStopsWhenTheLedgerReadFails pins that a read cut off partway
// through does not leave a signed bundle behind. A stream that ended early is
// a truncated export, and a truncated export that still writes its manifest is
// the silent short bundle this whole path exists to make impossible.
func TestExportStopsWhenTheLedgerReadFails(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	_, err = Export(context.Background(), &failingRowSource{}, privateKey, "ed25519:test",
		exportTestFilter(orgID), dir)
	if err == nil {
		t.Fatal("export reported success on a ledger read that failed")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "manifest.json")); statErr == nil {
		t.Fatal("a failed export left a signed manifest behind")
	}
}

// failingRowSource hands over one row and then reports the read failed, the
// shape of a connection dropped partway through a large export.
type failingRowSource struct{}

func (*failingRowSource) StreamQuery(_ context.Context, _ QueryFilter, _ RowVisitor) error {
	return context.DeadlineExceeded
}

// TestFailedReExportLeavesThePublishedBundleIntact pins what a re-export may
// destroy, which is nothing. An operator re-exporting into a directory that
// already holds a bundle is the ordinary case, and the export can fail partway
// for reasons that have nothing to do with the bundle sitting there: a dropped
// connection, a full disk, a cancelled command. Two earlier shapes of this code
// damaged that bundle. Writing rows straight to events.jsonl truncated it at the
// first byte, leaving the old signed manifest describing rows that were gone.
// Removing the manifest first destroyed the only thing that makes the directory
// readable as a bundle, for the whole length of an export that then failed.
// Either way the operator loses evidence they still had a moment earlier, and
// the second one loses it silently.
func TestFailedReExportLeavesThePublishedBundleIntact(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const publishedRows = 40
	source := &ledgerRowStream{
		t: t, orgID: orgID, rows: publishedRows, shards: 4,
		base: time.Now().UTC().Add(-publishedRows * time.Second), observe: nil,
	}

	published, err := Export(
		context.Background(), source, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	rowsBefore, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("read published rows: %v", err)
	}

	if _, err := Export(context.Background(), &failingRowSource{}, privateKey, "ed25519:test",
		exportTestFilter(orgID), dir); err == nil {
		t.Fatal("the re-export reported success on a ledger read that failed")
	}

	rowsAfter, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatalf("the failed re-export destroyed the published rows: %v", err)
	}
	if !bytes.Equal(rowsBefore, rowsAfter) {
		t.Fatalf("the failed re-export rewrote the published rows: %d bytes before, %d after",
			len(rowsBefore), len(rowsAfter))
	}

	// The surviving pair still has to verify. Rows that survived beside a
	// manifest that did not would be the same defect wearing the other mask.
	report, err := VerifyBundle(dir, publicKey)
	if err != nil {
		t.Fatalf("the bundle that survived the failed re-export no longer verifies: %v", err)
	}
	if verdict := report.Err(); verdict != nil {
		t.Fatalf("surviving bundle failed verification: %v", verdict)
	}
	if report.RowsScanned != published.RowCount {
		t.Fatalf("surviving bundle holds %d rows, want the %d it was published with",
			report.RowsScanned, published.RowCount)
	}

	// Nothing staged may be left behind either: a leftover partial file grows
	// the directory every time an export fails.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read bundle dir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), stagedSuffix) {
			t.Fatalf("the failed re-export left %s behind", entry.Name())
		}
	}
}
