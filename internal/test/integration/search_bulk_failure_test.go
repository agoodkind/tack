package integration

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// releasedWorkWait bounds the wait for released work to become claimable
// after its retry delay.
const releasedWorkWait = time.Minute

// rewriteSearchGeneration indexes page again at its own external version
// with search_generation stored as text or as a number. Stored text makes
// the guarded access script fail for that one bulk item.
func rewriteSearchGeneration(t *testing.T, client *opensearchapi.Client, index string, page searchPageSource, asText bool) {
	t.Helper()
	value := pageGeneration(t, page)
	generation := json.RawMessage(strconv.FormatInt(value, 10))
	if asText {
		generation = mustJSON(strconv.FormatInt(value, 10))
	}
	document := map[string]json.RawMessage{
		"node_id": mustJSON(page.NodeID), "node_type": mustJSON(page.NodeType), "name": mustJSON(page.Name),
		"node_revision": mustJSON(page.NodeRevision), "projection_version": mustJSON(page.ProjectionVersion),
		"page_ordinal": mustJSON(page.PageOrdinal), "page_text": mustJSON(*page.PageText), "retired": json.RawMessage("false"),
		"search_generation": generation, "access": mustJSON(page.Access),
	}
	requireBulkSucceeded(t, nativeBulk(t, client, nativeIndexAction(t, index, page.ID, int(value), encodeNativeJSON(t, document))))
}

// TestSearchPartialAccessFailureResumes requires the store to checkpoint only
// the accepted prefix when one item in the middle of an access-only batch
// fails. The retried work must resume after that prefix.
func TestSearchPartialAccessFailureResumes(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	moved := newMovableChild(t, stores, strings.Repeat("partial bulk page text ", 12))
	worker := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(accessPageBytes))
	runSearchWorkerUntilIdle(t, worker)
	before := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	if len(before) < 4 {
		t.Fatalf("child has %d pages, want at least four", len(before))
	}
	rewriteSearchGeneration(t, client, index, before[2], true)
	moveChild(t, stores, moved)

	failed := false
	for range 50 {
		claimed, err := worker.RunSlice(t.Context())
		if err != nil {
			failed = true
			break
		}
		if !claimed {
			break
		}
	}
	if !failed {
		t.Fatal("the access batch with a failing item succeeded")
	}
	// The check skips the pages after the failed item. OpenSearch applies
	// each bulk item separately, and those pages can receive the update in
	// the same request. The retry leaves them unchanged because their
	// generation already matches.
	partial := searchNodePages(t, client, index, moved.Fixture.NodeID, false)
	for position, page := range partial[:3] {
		updated := !slices.Equal(page.Access.Keys, before[position].Access.Keys)
		if updated != (position < 2) {
			t.Fatalf("page %d updated = %t, want the first two pages updated and the failed page unchanged", position, updated)
		}
	}
	store := stores.SearchWork(source)
	var pending searchdomain.Work
	released := waitFor(t, releasedWorkWait, func() bool {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassAccess, "inspector", shortLease)
		if errors.Is(err, searchdomain.ErrNoWork) {
			return false
		}
		if err != nil {
			t.Fatalf("claim access work: %v", err)
		}
		pending = work
		return work.NodeID == moved.Fixture.NodeID
	})
	if !released || pending.Phase != searchdomain.PhasePages || pending.Cursor == "" {
		t.Fatalf("pending access work = %+v, want the pages phase with the checkpoint after the accepted prefix", pending)
	}
	waitPastLeases(t, pending)
	rewriteSearchGeneration(t, client, index, before[2], false)
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, searchWorkerSettings(accessPageBytes)))
	for position, page := range searchNodePages(t, client, index, moved.Fixture.NodeID, false) {
		if slices.Equal(page.Access.Keys, before[position].Access.Keys) {
			t.Fatalf("page %d kept its old access after the retry", position)
		}
	}
}

// TestSearchRejectsInvalidPageText requires validation to reject a page above
// 4,096 bytes and an active page without page_text before any indexing
// request.
func TestSearchRejectsInvalidPageText(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	fixture := putSearchText(t, stores, "valid text", readerExcludedValue)
	store := stores.SearchWork(clock.Wall{})
	work, err := store.Claim(t.Context(), searchdomain.WorkClassLive, "validator", time.Minute)
	for err == nil && work.NodeID != fixture.NodeID {
		work, err = store.Claim(t.Context(), searchdomain.WorkClassLive, "validator", time.Minute)
	}
	if err != nil {
		t.Fatalf("claim live work: %v", err)
	}
	page := readSearchPages(t, stores, fixture.NodeID, searchdomain.MaxPageBytes)[0]
	for name, text := range map[string]string{"oversized": strings.Repeat("a", searchdomain.MaxPageBytes+1), "missing": ""} {
		invalid := page
		invalid.Text = text
		if _, err := store.Register(t.Context(), work, invalid); err == nil {
			t.Fatalf("register accepted the %s page", name)
		}
		documentID := searchdomain.DocumentID(work.OrgID, work.NodeID, invalid.Revision, invalid.ProjectionVersion, invalid.Ordinal)
		intent := searchdomain.WriteIntent{Work: work, Page: invalid, DocumentID: documentID}
		if _, err := adapter.Put(t.Context(), intent); err == nil {
			t.Fatalf("put accepted the %s page", name)
		}
		response, _ := client.Document.Get(t.Context(), opensearchapi.DocumentGetReq{Index: index, DocumentID: documentID})
		if response != nil && response.Found {
			t.Fatalf("OpenSearch stored the %s page", name)
		}
	}
}
