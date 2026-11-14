// export_source_helpers_test.go provides row sources and filters for export tests.

package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ledgerRowStream generates correctly chained rows individually without
// retaining them after the visitor returns. Memory measurements then exclude
// a fixture slice containing the whole ledger.
type ledgerRowStream struct {
	t      *testing.T
	orgID  uuid.UUID
	rows   int
	shards int
	base   time.Time
	// observe samples export state after each visitor call.
	observe func(index int)
}

// StreamQuery generates interleaved shards with contiguous ascending sequences.
// It honors filter limits to detect an export that requests only a page. A
// limited fixture returns the oldest rows rather than the ledger's newest page;
// the test checks completeness, not which incomplete page an export requests.
func (s *ledgerRowStream) StreamQuery(
	_ context.Context,
	filter QueryFilter,
	visit RowVisitor,
) error {
	s.t.Helper()
	rowBudget := filteredRowBudget(filter, s.rows)
	seqByShard := make([]int64, s.shards)
	prevByShard := make([][]byte, s.shards)

	for index := range rowBudget {
		shard := index % s.shards
		seqByShard[shard]++
		row := Row{
			OrgID: s.orgID,
			EventTime: s.base.Add(time.Duration(index) * time.Millisecond).Truncate(
				time.Microsecond,
			),
			EventID:   uuid.Must(uuid.NewV7()),
			Seq:       seqByShard[shard],
			Shard:     int16(shard),
			ActorID:   uuid.Must(uuid.NewV7()),
			ActorKind: 1,
			Action:    "auth.token_used",
			Outcome:   OutcomeOK,
			// Include realistic payload sizes to distinguish row accumulation
			// from bounded streaming memory in the budget assertions.
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

// filteredRowBudget applies appendAuditQueryOrder's limit rule. Zero selects
// all rows; a positive limit caps the count; a negative limit uses the page default.
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

// sliceRowSource visits existing rows in slice order, including orders the
// database can return without sorting.
type sliceRowSource struct {
	rows []Row
}

func (s *sliceRowSource) StreamQuery(
	_ context.Context,
	filter QueryFilter,
	visit RowVisitor,
) error {
	rowBudget := filteredRowBudget(filter, len(s.rows))
	for _, row := range s.rows[:rowBudget] {
		if err := visit(row); err != nil {
			return err
		}
	}

	return nil
}

// exportTestFilter selects the whole range without a limit for compliance
// exports and restore drills. Zero must select every matching row, not a page.
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
