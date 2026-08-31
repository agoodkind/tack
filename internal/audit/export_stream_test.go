package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
//
// The filter's limit is honoured the way the ledger honours it, because a
// fixture that returned every row whatever it was asked for would answer an
// export that quietly took a page with the whole ledger, and the page-size
// assertion would hold against the very defect it names. The rows a capped read
// hands over are the oldest rather than the newest page the ledger would order
// and return: what an export must not do is take a page at all, and which page
// it would have taken changes nothing here.
func (s *ledgerRowStream) StreamQuery(_ context.Context, filter QueryFilter, visit RowVisitor) error {
	s.t.Helper()
	rowBudget := filteredRowBudget(filter, s.rows)
	seqByShard := make([]int64, s.shards)
	prevByShard := make([][]byte, s.shards)
	for index := range rowBudget {
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

// filteredRowBudget is how many rows a source may hand over under filter,
// resolved the way the ledger resolves it: a zero limit is every matching row,
// a positive limit caps at that many, and a negative one is a caller mistake
// that normalises to the page default rather than meaning unlimited. That is
// appendAuditQueryOrder's rule, and a fixture that read the limit differently
// would prove the export against a ledger that does not exist.
func filteredRowBudget(filter QueryFilter, available int) int {
	limit := filter.Limit
	if limit < 0 {
		limit = auditQueryPageDefault
	}
	if limit > 0 && limit < available {
		return limit
	}
	return available
}

// sliceRowSource hands over rows a test already built, in the order the slice
// holds them. It is how the ordering assertion drives the export with a row
// order the database is free to return.
type sliceRowSource struct {
	rows []Row
}

func (s *sliceRowSource) StreamQuery(_ context.Context, filter QueryFilter, visit RowVisitor) error {
	rowBudget := filteredRowBudget(filter, len(s.rows))
	for _, row := range s.rows[:rowBudget] {
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

// TestExportWritesEveryRowPastEveryPageSize covers the page-size half of the
// defect this file exists for. The export used to read its rows through the
// page-sized query, which defaults a zero limit to one page, so a whole-range
// export came back holding a page and the bundle still read as complete: the
// manifest counts what was written, the digest covers what was written, and
// nothing in the bundle records what was left behind. The fixture honours the
// filter's limit, so an export that asked for a page gets a page and fails here.
//
// The other half of that defect, holding every row before writing any of them,
// is invisible to every assertion below: an export that collected its rows first
// writes the same bytes and signs the same manifest. It is a memory property, and
// TestExportPeakMemoryIsFlatInRowCount is what pins it, by sampling the heap
// while the export is still running.
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
	// The manifest is signed over the row count and the digest, so a bundle that
	// verifies is one whose count and digest both describe the rows the file
	// actually holds, rather than a manifest signed for a read that came back
	// short.
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
	scrambledRows := scrambleRowOrder(chained)
	// The order is the fixture this test rests on, so it is checked before it is
	// exported. A scramble that quietly left the shards in blocks would make
	// every assertion below pass for the wrong reason.
	assertShardsInterleaved(t, scrambledRows)

	inFileOrder := exportedVerifyReport(t, orgID, chained)
	scrambled := exportedVerifyReport(t, orgID, scrambledRows)

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
// directly: the shards are interleaved, so no two consecutive rows come from
// the same one, and each shard's own sequence arrives rotated and reversed.
// This is the worst order a database with no ORDER BY could hand back.
//
// Reversing the whole slice instead leaves every shard in one contiguous block,
// because the rows are built shard-major. A bundle in that order is the shape
// the export already writes, so the verdict it earns says nothing about the
// interleaving this test names.
func scrambleRowOrder(rows []Row) []Row {
	pending := map[int16][]Row{}
	shardOrder := make([]int16, 0)
	for _, row := range rows {
		if _, seen := pending[row.Shard]; !seen {
			shardOrder = append(shardOrder, row.Shard)
		}
		pending[row.Shard] = append(pending[row.Shard], row)
	}
	for _, shard := range shardOrder {
		// A different rotation per shard so the shards are not phase-aligned
		// either: their sequences wrap at different points in the file.
		pending[shard] = rotatedReverse(pending[shard], int(shard)+1)
	}

	scrambled := make([]Row, 0, len(rows))
	for len(scrambled) < len(rows) {
		for _, shard := range shardOrder {
			remaining := pending[shard]
			if len(remaining) == 0 {
				continue
			}
			scrambled = append(scrambled, remaining[0])
			pending[shard] = remaining[1:]
		}
	}
	return scrambled
}

// rotatedReverse returns one shard's rows backwards and rotated by offset, so
// the sequence does not merely descend: it runs backwards, wraps once, and runs
// backwards again. A plain reversal is still monotonic, and monotonic is an
// order a chain walk can special-case its way through.
func rotatedReverse(rows []Row, offset int) []Row {
	if len(rows) == 0 {
		return rows
	}
	reversed := make([]Row, 0, len(rows))
	for i := len(rows) - 1; i >= 0; i-- {
		reversed = append(reversed, rows[i])
	}
	pivot := offset % len(reversed)
	rotated := make([]Row, 0, len(reversed))
	rotated = append(rotated, reversed[pivot:]...)
	rotated = append(rotated, reversed[:pivot]...)
	return rotated
}

// assertShardsInterleaved fails when the scrambled order still hands a shard's
// rows over in one block, or hands any shard over in sequence order. The order
// is the whole subject of the test that calls this: an order that keeps each
// shard contiguous and ascending is the order the export itself writes, and a
// verdict earned on that order says nothing about a database that interleaves.
func assertShardsInterleaved(t *testing.T, rows []Row) {
	t.Helper()
	for i := 1; i < len(rows); i++ {
		if rows[i].Shard == rows[i-1].Shard {
			t.Fatalf("rows %d and %d both come from shard %d, so the shards are not interleaved",
				i-1, i, rows[i].Shard)
		}
	}
	seqByShard := map[int16][]int64{}
	for _, row := range rows {
		seqByShard[row.Shard] = append(seqByShard[row.Shard], row.Seq)
	}
	for shard, sequences := range seqByShard {
		if slices.IsSorted(sequences) {
			t.Fatalf("shard %d arrives in sequence order, so the file order is one a chain walk could use directly",
				shard)
		}
	}
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
//
// Both halves of that failure space run. A read that fails before the first row
// leaves the writer with nothing, which is the easy half: no bytes were ever at
// risk. A read that fails after rows have already been encoded and flushed is
// the half where an export has something to damage, and it is the shape a
// dropped connection actually takes on a large ledger.
func TestExportStopsWhenTheLedgerReadFails(t *testing.T) {
	cases := []struct {
		name              string
		rowsBeforeFailure int
	}{
		{name: "before any row reaches the export", rowsBeforeFailure: 0},
		{name: "after rows are already written", rowsBeforeFailure: exportFailureRows},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			orgID := uuid.Must(uuid.NewV7())
			_, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}

			source := &failingRowSource{t: t, orgID: orgID, rowsBeforeFailure: testCase.rowsBeforeFailure}
			if _, err := Export(context.Background(), source, privateKey, "ed25519:test",
				exportTestFilter(orgID), dir); err == nil {
				t.Fatal("export reported success on a ledger read that failed")
			}
			if _, statErr := os.Stat(filepath.Join(dir, exportManifestFile)); statErr == nil {
				t.Fatal("a failed export left a signed manifest behind")
			}
			assertDirectoryHoldsOnlyItsBundle(t, dir)
		})
	}
}

// exportFailureRows is how many rows the failing reads hand over before they
// fail. It is sized so the export has flushed its write buffer to disk before
// the failure arrives, which the re-export test below checks rather than
// assumes; a failure the writer absorbs entirely in memory never exercises what
// a partial export does to the file.
const exportFailureRows = 3000

// failingRowSource hands over rowsBeforeFailure rows and then reports the read
// failed, the shape of a connection dropped partway through a large export.
//
// The row count is the point. A source that fails before handing over anything
// tests only that a read which produced nothing publishes nothing, and every
// implementation that damages the bundle once rows start arriving passes that.
type failingRowSource struct {
	t                 *testing.T
	orgID             uuid.UUID
	rowsBeforeFailure int
}

func (s *failingRowSource) StreamQuery(ctx context.Context, filter QueryFilter, visit RowVisitor) error {
	s.t.Helper()
	if s.rowsBeforeFailure > 0 {
		delivered := &ledgerRowStream{
			t: s.t, orgID: s.orgID, rows: s.rowsBeforeFailure, shards: 4,
			base:    time.Now().UTC().Add(-time.Duration(s.rowsBeforeFailure) * time.Second),
			observe: nil,
		}
		if err := delivered.StreamQuery(ctx, filter, visit); err != nil {
			return err
		}
	}
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
//
// The failing re-export hands over rows before it fails, because a re-export
// that never produced a row cannot damage anything: it is the rows arriving
// that put the published bundle at risk, so a read that fails first would leave
// every implementation of this path looking correct.
func TestFailedReExportLeavesThePublishedBundleIntact(t *testing.T) {
	dir := t.TempDir()
	orgID := uuid.Must(uuid.NewV7())
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	source := &ledgerRowStream{
		t: t, orgID: orgID, rows: exportFailureRows, shards: 4,
		base: time.Now().UTC().Add(-exportFailureRows * time.Second), observe: nil,
	}

	published, err := Export(
		context.Background(), source, privateKey, "ed25519:test", exportTestFilter(orgID), dir)
	if err != nil {
		t.Fatalf("first export: %v", err)
	}
	_, rowsBefore := publishedBundle(t, dir)
	// The failing re-export below hands over this same number of rows, so this
	// is what establishes that its rows were flushed to disk rather than held in
	// the write buffer and discarded. A failure the buffer absorbed would never
	// have touched the filesystem, and the assertions after it would hold for a
	// reason that has nothing to do with staging.
	if len(rowsBefore) <= exportWriteBufferBytes {
		t.Fatalf("%d rows encode to %d bytes, which the %d-byte write buffer holds entirely; the failed re-export would never reach the disk",
			exportFailureRows, len(rowsBefore), exportWriteBufferBytes)
	}

	failing := &failingRowSource{t: t, orgID: orgID, rowsBeforeFailure: exportFailureRows}
	if _, err := Export(context.Background(), failing, privateKey, "ed25519:test",
		exportTestFilter(orgID), dir); err == nil {
		t.Fatal("the re-export reported success on a ledger read that failed")
	}

	_, rowsAfter := publishedBundle(t, dir)
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

	assertDirectoryHoldsOnlyItsBundle(t, dir)
}
