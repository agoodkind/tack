package integration

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// shortenedValue projects to one page at the recovery page size.
const shortenedValue = "short final text"

// TestSearchRecoveryShorteningRetiresRemovedPages indexes a multi-page node,
// lets an old owner write page 0 of a second long revision and register
// page 1 without writing it, then shortens the node to one page. After the
// worker finishes, every document of both long revisions must be retired
// with no text, the active documents must equal the one-page revision, and
// the delayed write of the removed page must fail as obsolete.
func TestSearchRecoveryShorteningRetiresRemovedPages(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	settings := searchWorkerSettings(recoveryPageBytes)
	fixture := putSearchText(t, stores, readerIncludedValue(), readerExcludedValue)
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, settings))
	original := searchNodePages(t, client, index, fixture.NodeID, false)
	if len(original) < 3 {
		t.Fatalf("original revision indexed %d pages, want several", len(original))
	}

	writeSearchNode(t, stores, fixture, readerIncludedValue()+" second revision", readerExcludedValue)
	workStore := stores.SearchWork(source)
	oldWork, err := workStore.Claim(t.Context(), searchdomain.WorkClassLive, "old-owner", settings.Lease)
	if err != nil || oldWork.NodeID != fixture.NodeID {
		t.Fatalf("claim the second revision = %+v err %v, want %s", oldWork, err, fixture.NodeID)
	}
	reader := stores.SearchContent(stores.SearchPolicySet())
	request := searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil,
		MaxBytes: recoveryPageBytes, SearchGeneration: oldWork.Generation,
	}
	first, err := reader.Content(t.Context(), request)
	if err != nil {
		t.Fatalf("old owner read page 0: %v", err)
	}
	firstIntent, err := workStore.Register(t.Context(), oldWork, first)
	if err != nil {
		t.Fatalf("old owner register page 0: %v", err)
	}
	if _, err := adapter.Put(t.Context(), firstIntent); err != nil {
		t.Fatalf("old owner write page 0: %v", err)
	}
	if err := workStore.CompletePage(t.Context(), firstIntent, first.NextCursor, first.Done); err != nil {
		t.Fatalf("old owner checkpoint page 0: %v", err)
	}
	oldWork.Cursor, oldWork.Ordinal, oldWork.Projection = first.NextCursor, first.Ordinal+1, first.ProjectionVersion
	request.Cursor = first.NextCursor
	second, err := reader.Content(t.Context(), request)
	if err != nil {
		t.Fatalf("old owner read page 1: %v", err)
	}
	delayed, err := workStore.Register(t.Context(), oldWork, second)
	if err != nil {
		t.Fatalf("old owner register page 1: %v", err)
	}

	writeSearchNode(t, stores, fixture, shortenedValue, readerExcludedValue)
	released := searchdomain.Failure{Message: "request delayed", Counted: false}
	if err := workStore.Release(t.Context(), oldWork, released); err != nil && !errors.Is(err, searchdomain.ErrWorkChanged) {
		t.Fatalf("release the old owner: %v", err)
	}
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, settings))

	shortened := readSearchPages(t, stores, fixture.NodeID, recoveryPageBytes)
	if len(shortened) != 1 {
		t.Fatalf("shortened node has %d pages, want 1", len(shortened))
	}
	requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), shortened, oldWork.Generation)
	for _, document := range original {
		requireRetiredDocument(t, client, index, document.ID)
	}
	requireRetiredDocument(t, client, index, firstIntent.DocumentID)
	if _, err := adapter.Put(t.Context(), delayed); !errors.Is(err, searchdomain.ErrObsoleteWrite) {
		t.Fatalf("delayed write of removed page 1 = %v, want obsolete write", err)
	}
	requireRetiredDocument(t, client, index, delayed.DocumentID)
}

// requireRetiredDocument requires the document to exist as a retired record
// with no page text and no generated semantic field.
func requireRetiredDocument(t *testing.T, client *opensearchapi.Client, index, documentID string) {
	t.Helper()
	response, err := client.Document.Get(t.Context(), opensearchapi.DocumentGetReq{Index: index, DocumentID: documentID})
	if err != nil || !response.Found {
		t.Fatalf("read document %s: found %t err %v", documentID, err == nil && response.Found, err)
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(response.Source, &source); err != nil {
		t.Fatalf("decode document %s: %v", documentID, err)
	}
	if string(source["retired"]) != "true" || len(source["page_text"]) != 0 || len(source["page_text_semantic_info"]) != 0 {
		t.Fatalf("document %s is not a retired record without text", documentID)
	}
}
