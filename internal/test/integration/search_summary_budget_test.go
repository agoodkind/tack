package integration

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// summaryReadMessage is the record that telemetry.FDBOp logs once for
	// each node summary read.
	summaryReadMessage = "store.node_summary.read"
	// budgetValidNodes stays below the 25-result page limit. A response that
	// does not complete then reads all of its four raw batches.
	budgetValidNodes = 20
	// budgetDeletedPages exceeds eight raw batches of 100 page matches, so
	// the first two responses read four batches each and the second one is
	// a continuation that a replay can repeat.
	budgetDeletedPages = 900
	budgetMaxBatches   = 4
	budgetMaxResults   = 25
	budgetBatchSize    = 100
	budgetMaxResponses = 20
	// budgetResponseBytes is the response byte budget of the search service.
	budgetResponseBytes = 16384
)

// summaryReadCounter is a slog handler that counts summary read records and
// passes every record to the default handler, so a failing run keeps its
// logs.
type summaryReadCounter struct {
	reads *atomic.Int64
	next  slog.Handler
}

func newSummaryReadCounter() summaryReadCounter {
	return summaryReadCounter{reads: &atomic.Int64{}, next: slog.Default().Handler()}
}

// Enabled accepts every level: summary reads log at debug level, which the
// default handler can disable.
func (c summaryReadCounter) Enabled(context.Context, slog.Level) bool { return true }

func (c summaryReadCounter) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == summaryReadMessage {
		c.reads.Add(1)
	}
	if !c.next.Enabled(ctx, record.Level) {
		return nil
	}
	if err := c.next.Handle(ctx, record); err != nil {
		slog.ErrorContext(ctx, "test.summary_counter.forward_failed", slog.String("err", err.Error()))
		return fmt.Errorf("forward log record %q: %w", record.Message, err)
	}
	return nil
}

func (c summaryReadCounter) WithAttrs(attrs []slog.Attr) slog.Handler {
	return summaryReadCounter{reads: c.reads, next: c.next.WithAttrs(attrs)}
}

func (c summaryReadCounter) WithGroup(name string) slog.Handler {
	return summaryReadCounter{reads: c.reads, next: c.next.WithGroup(name)}
}

// countedSearch runs one production search call with a logger that counts
// summary reads and returns the number of summary reads the call made.
func countedSearch(t *testing.T, counter summaryReadCounter, searcher *service.SearchQueryService, request service.SearchRequest) (service.SearchPage, int64) {
	t.Helper()
	ctx := telemetry.WithLogger(t.Context(), slog.New(counter))
	start := counter.reads.Load()
	page, err := searcher.Search(ctx, request)
	if err != nil {
		t.Fatalf("search session %s version %d: %v", request.SessionID, request.Version, err)
	}
	return page, counter.reads.Load() - start
}

// pageResultBytes is the rendered byte bound of the results of one page.
func pageResultBytes(page service.SearchPage) int {
	total := 0
	for _, result := range page.Results {
		total += service.ResultBytes(result)
	}
	return total
}

func pageResultIDs(page service.SearchPage) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(page.Results))
	for _, result := range page.Results {
		ids = append(ids, result.ID)
	}
	return ids
}

// requireRankedBeforeValid requires every invalid node to appear in the raw
// ranker order before the first valid node, and enough raw matches for two
// responses that read four batches each.
func requireRankedBeforeValid(t *testing.T, fixture queryFixture, filter searchdomain.AccessFilter, invalid, valid []uuid.UUID) {
	t.Helper()
	ranked := rawRankedNodes(t, fixture, filter, scaleQuery)
	if len(ranked) <= 2*budgetMaxBatches*budgetBatchSize {
		t.Fatalf("raw ranking returned %d page matches, want more than %d", len(ranked), 2*budgetMaxBatches*budgetBatchSize)
	}
	firstValid := slices.IndexFunc(ranked, func(id uuid.UUID) bool { return slices.Contains(valid, id) })
	for _, nodeID := range invalid {
		position := slices.Index(ranked, nodeID)
		if position < 0 || (firstValid >= 0 && position > firstValid) {
			t.Fatalf("invalid node %s ranks at %d and the first valid node at %d, want the invalid node first", nodeID, position, firstValid)
		}
	}
}

// TestSearchSummaryReadsPerBatch ranks a deleted node with 900 duplicate
// pages and a foreign node with corrupted indexed access before 20 valid
// nodes. Each response must make at most four summary reads, exactly four
// when it neither completes nor fills 25 results, and each replay exactly
// one with the same result IDs. Search must omit the deleted and foreign
// nodes and return every valid node once.
func TestSearchSummaryReadsPerBatch(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace, foreign := fixture.Workspaces[0], otherOrganization(t, fixture)
	entryID := entryPoint(t, fixture, workspace)
	kind, foreignKind := putOpaqueKind(t, fixture, workspace.OrgID), putOpaqueKind(t, fixture, foreign.OrgID)
	valid := make([]uuid.UUID, 0, budgetValidNodes)
	for number := range budgetValidNodes {
		valid = append(valid, putOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Ledger item %d", number), scaleQuery, "excluded"))
	}
	forbidden := putOpaqueNode(t, fixture, foreignKind, entryPoint(t, fixture, foreign), "Orbital manifest checklist",
		strings.Repeat(scaleQuery+". ", 3), "excluded")
	deleted := putOpaqueNode(t, fixture, kind, entryID, "Orbital duplicate", scaleQuery, "excluded")
	drainSearchWork(t, fixture.Worker, 2000)
	filter := callerAccess(t, fixture, workspace, entryID)
	putDuplicatePages(t, fixture, kind, deleted, filter, budgetDeletedPages)
	corruptIndexedAccess(t, fixture, forbidden, filter)
	if err := fixture.Stores.Nodes.Delete(t.Context(), kind.OrgID, deleted); err != nil {
		t.Fatalf("delete node %s: %v", deleted, err)
	}
	requireRankedBeforeValid(t, fixture, filter, []uuid.UUID{deleted, forbidden}, valid)

	searcher := queryService(t, fixture, budgetResponseBytes, 15*time.Minute, 2*time.Hour)
	counter := newSummaryReadCounter()
	request := serviceRequest(t, fixture, scaleQuery)
	returned := make([]uuid.UUID, 0, budgetValidNodes)
	fullBudgetResponses, replays, complete := 0, 0, false
	for range budgetMaxResponses {
		page, reads := countedSearch(t, counter, searcher, request)
		if reads > budgetMaxBatches {
			t.Fatalf("one response made %d summary reads, want at most %d", reads, budgetMaxBatches)
		}
		if !page.Complete && len(page.Results) < budgetMaxResults {
			if reads != budgetMaxBatches {
				t.Fatalf("the response stopped on the %d-byte response budget after %d results (%d result bytes) and %d of %d summary reads; the corpus must not fill the byte budget",
					budgetResponseBytes, len(page.Results), pageResultBytes(page), reads, budgetMaxBatches)
			}
			fullBudgetResponses++
		}
		if request.SessionID != uuid.Nil && !page.Complete {
			replayed, replayReads := countedSearch(t, counter, searcher, request)
			if replayReads != 1 || !slices.Equal(pageResultIDs(replayed), pageResultIDs(page)) {
				t.Fatalf("replay made %d summary reads and returned %v, want 1 read and %v", replayReads, pageResultIDs(replayed), pageResultIDs(page))
			}
			replays++
		}
		returned = append(returned, pageResultIDs(page)...)
		if page.Complete {
			complete = true
			break
		}
		request = continued(request, page)
	}
	if !complete {
		t.Fatalf("search did not complete within %d responses", budgetMaxResponses)
	}
	if fullBudgetResponses < 2 || replays < 1 {
		t.Fatalf("traversal had %d four-batch responses and %d replays, want at least 2 and 1", fullBudgetResponses, replays)
	}
	if slices.Contains(returned, deleted) || slices.Contains(returned, forbidden) {
		t.Fatalf("search returned the deleted node %s or the foreign node %s", deleted, forbidden)
	}
	requireCorpusOnce(t, returned, valid, entryID)
}
