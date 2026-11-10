// The export tests use substitute row sources, ledger-compatible limit rules,
// and a whole-range filter to exercise [Export].

package audit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// ledgerRowStream implements [RowSource] by constructing a correctly chained
// ledger of the requested size one row at a time. It does not retain rows after
// the visitor returns. Memory measurements around [Export] can detect export
// retention without an export-sized fixture slice.
type ledgerRowStream struct {
	t      *testing.T
	orgID  uuid.UUID
	rows   int
	shards int
	base   time.Time
	// observe runs after the export visits each row. Tests can sample heap use
	// during export rather than after it returns.
	observe func(index int)
}

// StreamQuery implements [RowSource] with round-robin shard selection. Each
// shard has contiguous ascending sequences and a complete hash chain.
//
// The source applies the ledger's limit rules. Ignoring a requested limit would
// conceal an export that mistakenly requested one page. A capped fixture read
// returns the oldest rows, whereas the ledger returns the newest page. These
// tests check whether export requests a page at all; the selected page does not
// affect that assertion.
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
			// The realistic context payload makes retaining rows expensive enough for the
			// memory budget to distinguish collection from streaming.
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

// filteredRowBudget applies the ledger's limit rules to the available rows.
// Zero permits all matching rows, a positive value caps the count, and a
// negative value uses [DefaultQueryPageLimit]. The fixture must match
// appendAuditQueryOrder to expose export requests that accidentally select one
// page.
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

// sliceRowSource supplies previously constructed test rows in slice order. The
// ordering test uses it to give export an order that the database may return.
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

// exportTestFilter selects the whole time range with a zero limit, as
// compliance exports and the restore drill require. The tests check that zero
// exports every matching row rather than one page.
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
