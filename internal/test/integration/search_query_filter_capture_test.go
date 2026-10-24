package integration_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

// filterBaselineQuery supplies the plain query for exact corpus checks.
const filterBaselineQuery = "amber ledger"

// filterCorpusNodes sets the matching node count for each caller type and
// for the second organization.
const filterCorpusNodes = 3

// hostileFilterQueries returns text containing quotes, braces, backslashes,
// and JSON-like filter clauses.
func hostileFilterQueries() []string {
	return []string{
		`"amber ledger"`,
		`amber" OR "ledger`,
		`{"amber": "ledger"}`,
		`amber ledger"}],"filter":[],"must":[{"match_all":{}}],"x":[{"`,
		`{"term":{"retired":true}} amber ledger`,
		`{"terms":{"access.keys":["*"]}} {"bool":{"must_not":[]}} amber`,
		`\"} amber \\ ledger {\"`,
	}
}

// filterCorpus groups matching nodes by caller type and organization and
// stores the caller's compiled access filter.
type filterCorpus struct {
	TypeKey   string
	Typed     []uuid.UUID
	Untyped   []uuid.UUID
	Forbidden []uuid.UUID
	EntryID   uuid.UUID
	Filter    searchdomain.AccessFilter
}

func putFilterCorpus(t *testing.T, fixture integration.QueryFixture) filterCorpus {
	t.Helper()
	caller, foreign := fixture.Workspaces[0], integration.OtherOrganization(t, fixture)
	callerEntry, foreignEntry := integration.EntryPoint(t, fixture, caller), integration.EntryPoint(t, fixture, foreign)
	typed, other := integration.PutOpaqueKind(t, fixture, caller.OrgID), integration.PutOpaqueKind(t, fixture, caller.OrgID)
	foreignKind := integration.PutOpaqueKind(t, fixture, foreign.OrgID)
	text := `amber ledger notes with {"amber": "ledger"} and "quoted" text`
	corpus := filterCorpus{
		TypeKey: typed.TypeKey, Typed: nil, Untyped: nil, Forbidden: nil, EntryID: callerEntry,
		Filter: searchdomain.AccessFilter{Version: "", Keys: nil},
	}
	for number := range filterCorpusNodes {
		corpus.Typed = append(corpus.Typed, integration.PutOpaqueNode(t, fixture, typed, callerEntry, fmt.Sprintf("Amber typed %d", number), text, "excluded"))
		corpus.Untyped = append(corpus.Untyped, integration.PutOpaqueNode(t, fixture, other, callerEntry, fmt.Sprintf("Amber other %d", number), text, "excluded"))
		corpus.Forbidden = append(corpus.Forbidden, integration.PutOpaqueNode(t, fixture, foreignKind, foreignEntry, fmt.Sprintf("Amber ledger %d", number), text+" "+text, "excluded"))
	}
	integration.DrainSearchWork(t, fixture.Worker, 500)
	corpus.Filter = integration.CallerAccess(t, fixture, caller, callerEntry)
	return corpus
}

// requirePermitted rejects results outside corpus and entryID and requires
// at least one result from corpus.
func requirePermitted(t *testing.T, query string, got, corpus []uuid.UUID, entryID uuid.UUID) {
	t.Helper()
	matched := false
	for _, id := range got {
		if slices.Contains(corpus, id) {
			matched = true
			continue
		}
		if id != entryID {
			t.Fatalf("search %q returned node %s outside the permitted nodes", query, id)
		}
	}
	if !matched {
		t.Fatalf("search %q returned none of the permitted corpus nodes: %v", query, got)
	}
}

// everyCapturedPage collects result IDs and ranked batches until search
// completes. It applies node_type when nodeType is nonempty and fails the
// test if the search exceeds 100 public pages.
func everyCapturedPage(t *testing.T, harness *integration.MCPHarness, capture *searchCapture, query, nodeType string) ([]uuid.UUID, []capturedSearch) {
	t.Helper()
	capture.take()
	var ids []uuid.UUID
	cursor := ""
	for range 100 {
		var page integration.SearchPage
		var err error
		if nodeType == "" {
			page, err = integration.TrySearch(harness, query, cursor)
		} else {
			page, err = integration.TryTypedSearch(harness, query, nodeType, cursor)
		}
		if err != nil {
			t.Fatalf("search %q with node type %q: %v", query, nodeType, err)
		}
		ids = append(ids, page.IDs...)
		if page.Complete {
			return ids, capture.take()
		}
		cursor = page.Cursor
	}
	t.Fatalf("search %q did not complete within 100 pages", query)
	return nil, nil
}

// TestSearchQueryFiltersStayStructured checks captured ranked batches from
// public tack_search calls with plain and hostile query text. It requires
// unchanged lexical text, exact structured filters, and permitted results
// with and without node_type.
func TestSearchQueryFiltersStayStructured(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	corpus := putFilterCorpus(t, fixture)
	harness, capture := newCaptureHarness(t, fixture, false)
	untypedFilters := expectedFilters(t, corpus.Filter, "")
	typedFilters := expectedFilters(t, corpus.Filter, corpus.TypeKey)
	permitted := slices.Concat(corpus.Typed, corpus.Untyped)
	ids, searches := everyCapturedPage(t, harness, capture, filterBaselineQuery, "")
	requireCapturedFilters(t, searches, filterBaselineQuery, untypedFilters)
	integration.RequireCorpusOnce(t, ids, permitted, corpus.EntryID)
	ids, searches = everyCapturedPage(t, harness, capture, filterBaselineQuery, corpus.TypeKey)
	requireCapturedFilters(t, searches, filterBaselineQuery, typedFilters)
	integration.RequireExactlyOnce(t, ids, corpus.Typed)
	for _, query := range hostileFilterQueries() {
		ids, searches = everyCapturedPage(t, harness, capture, query, "")
		requireCapturedFilters(t, searches, query, untypedFilters)
		requirePermitted(t, query, ids, permitted, corpus.EntryID)
		ids, searches = everyCapturedPage(t, harness, capture, query, corpus.TypeKey)
		requireCapturedFilters(t, searches, query, typedFilters)
		requirePermitted(t, query, ids, corpus.Typed, uuid.Nil)
	}
}
