// export_source_helpers_test.go is how the export tests feed rows into Export:
// the row sources that stand in for the ledger, the limit rule they honour, and
// the whole-range filter they are driven with.

package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

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
		limit = DefaultQueryPageLimit
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
