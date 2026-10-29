package integration

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const recoveryPageBytes = 128

// TestSearchRecoveryAfterLeaseExpiry requires the production worker to finish
// indexing a node after the lease of the previous owner expires. The old
// owner must fail to checkpoint. The old owner stops after it registers a
// page, and the worker runs on reopened stores.
func TestSearchRecoveryAfterLeaseExpiry(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	included := readerIncludedValue()
	fixture := putSearchText(t, stores, included, readerExcludedValue)
	settings := searchWorkerSettings(recoveryPageBytes)
	oldStore := stores.SearchWork(source)
	oldWork, claimed, err := oldStore.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "old-owner", shortLease)
	if err != nil || !claimed {
		t.Fatalf("claim live work = claimed %t, err %v", claimed, err)
	}
	if oldWork.Target != index {
		t.Fatalf("claim target = %q, want serving index %q", oldWork.Target, index)
	}
	reader := stores.SearchContent(stores.SearchPolicySet())
	page, err := reader.Content(WithTestOperation(t, t.Context()), searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil,
		MaxBytes: recoveryPageBytes, SearchGeneration: oldWork.Generation,
	})
	if err != nil {
		t.Fatalf("old owner read: %v", err)
	}
	oldIntent, err := oldStore.Register(WithTestOperation(t, t.Context()), oldWork, page)
	if err != nil {
		t.Fatalf("old owner register: %v", err)
	}

	waitPastLeases(t, oldWork)
	restarted := reopenSearchStore(t)
	runSearchWorkerUntilIdle(t, newSearchWorker(t, restarted, adapter, source, settings))

	if err := oldStore.CompletePage(WithTestOperation(t, t.Context()), oldIntent, page.NextCursor, page.Done); !errors.Is(err, searchdomain.ErrWorkChanged) {
		t.Fatalf("expired owner checkpoint = %v, want changed work", err)
	}
	pages := readSearchPages(t, restarted, fixture.NodeID, recoveryPageBytes)
	requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), pages, oldWork.Generation)
}

// TestSearchRecoveryProjectionChangeBetweenOwners requires a second owner to
// fail with changed content when it registers the first page again after a
// projection change. The worker must then retire the document that the first
// owner wrote. The first owner writes that page and stops before its
// checkpoint.
func TestSearchRecoveryProjectionChangeBetweenOwners(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	fixture := putSearchText(t, stores, readerIncludedValue(), readerExcludedValue)
	settings := searchWorkerSettings(recoveryPageBytes)
	reader := stores.SearchContent(stores.SearchPolicySet())
	oldStore := stores.SearchWork(source)
	oldWork, claimed, err := oldStore.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "old-owner", shortLease)
	if err != nil || !claimed {
		t.Fatalf("claim live work = claimed %t, err %v", claimed, err)
	}
	request := searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil,
		MaxBytes: recoveryPageBytes, SearchGeneration: oldWork.Generation,
	}
	oldPage, err := reader.Content(WithTestOperation(t, t.Context()), request)
	if err != nil {
		t.Fatalf("old owner read: %v", err)
	}
	oldIntent, err := oldStore.Register(WithTestOperation(t, t.Context()), oldWork, oldPage)
	if err != nil {
		t.Fatalf("old owner register: %v", err)
	}
	if _, err := adapter.Put(WithTestOperation(t, t.Context()), oldIntent); err != nil {
		t.Fatalf("old owner write: %v", err)
	}

	putUnusedDefinition(t, stores, fixture.OrgID)
	waitPastLeases(t, oldWork)
	newStore := reopenSearchStore(t).SearchWork(source)
	newWork, claimed, err := newStore.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "new-owner", settings.Lease)
	if err != nil || !claimed || newWork.NodeID != fixture.NodeID || newWork.Ordinal != 0 {
		t.Fatalf("new owner claim = %+v claimed %t err %v, want a non-empty claim of the first page of %s", newWork, claimed, err, fixture.NodeID)
	}
	newPage, err := reader.Content(WithTestOperation(t, t.Context()), request)
	if err != nil || newPage.ProjectionVersion == oldPage.ProjectionVersion {
		t.Fatalf("new owner read projection %q err %v, want a projection other than %q", newPage.ProjectionVersion, err, oldPage.ProjectionVersion)
	}
	if _, err := newStore.Register(WithTestOperation(t, t.Context()), newWork, newPage); !errors.Is(err, node.ErrContentChanged) {
		t.Fatalf("second first-page registration = %v, want changed content", err)
	}

	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, source, settings))
	response, err := client.Document.Get(WithTestOperation(t, t.Context()), opensearchapi.GetReq{Index: index, ID: oldIntent.DocumentID})
	if err != nil || !response.Found {
		t.Fatalf("read the first owner's document: err=%v", err)
	}
	var first map[string]json.RawMessage
	if err := json.Unmarshal(response.Source, &first); err != nil {
		t.Fatalf("decode the first owner's document: %v", err)
	}
	if string(first["retired"]) != "true" || len(first["page_text"]) != 0 {
		t.Fatal("the first owner's page stayed searchable after the projection change")
	}
	pages := readSearchPages(t, stores, fixture.NodeID, recoveryPageBytes)
	requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), pages, oldWork.Generation)
}

// TestSearchDelayedWriter requires OpenSearch to reject a registered page
// write that the old worker sends after the node is deleted and its pages
// are retired.
// The retired document must stay free of text at the higher generation.
func TestSearchDelayedWriter(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	source := clock.Wall{}
	fixture := putSearchText(t, stores, "obsolete text", readerExcludedValue)
	workStore := stores.SearchWork(source)
	oldWork, claimed, err := workStore.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassLive, "old-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim live work = claimed %t, err %v", claimed, err)
	}
	page, err := stores.SearchContent(stores.SearchPolicySet()).Content(WithTestOperation(t, t.Context()), searchdomain.ContentRequest{
		NodeID: fixture.NodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: recoveryPageBytes, SearchGeneration: oldWork.Generation,
	})
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	oldRequest, err := workStore.Register(WithTestOperation(t, t.Context()), oldWork, page)
	if err != nil {
		t.Fatalf("register delayed page: %v", err)
	}
	if err := stores.Nodes.Delete(WithTestOperation(t, t.Context()), fixture.OrgID, fixture.NodeID); err != nil {
		t.Fatalf("delete node: %v", err)
	}
	delayed := searchdomain.Failure{Message: "request delayed", Counted: false}
	if err := workStore.Release(WithTestOperation(t, t.Context()), oldWork, delayed); err != nil && !errors.Is(err, searchdomain.ErrWorkChanged) {
		t.Fatalf("release delayed work: %v", err)
	}
	deletion, claimed, err := workStore.Claim(WithTestOperation(t, t.Context()), searchdomain.WorkClassCleanup, "new-worker", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("claim deletion cleanup = claimed %t, err %v", claimed, err)
	}
	if !deletion.Deleted || deletion.NodeID != fixture.NodeID || deletion.Generation <= oldWork.Generation {
		t.Fatalf("deletion work = %+v, want a later deleted cleanup of %s", deletion, fixture.NodeID)
	}
	worker := newSearchWorker(t, stores, adapter, source, searchWorkerSettings(recoveryPageBytes))
	if err := worker.Process(WithTestOperation(t, t.Context()), deletion); err != nil {
		t.Fatalf("process deletion cleanup: %v", err)
	}
	if _, err := adapter.Put(WithTestOperation(t, t.Context()), oldRequest); !errors.Is(err, searchdomain.ErrObsoleteWrite) {
		t.Fatalf("delayed write error = %v, want obsolete write", err)
	}
	response, err := client.Document.Get(WithTestOperation(t, t.Context()), opensearchapi.GetReq{Index: index, ID: oldRequest.DocumentID})
	if err != nil || !response.Found {
		t.Fatalf("read retired document: err=%v", err)
	}
	var retired map[string]json.RawMessage
	if err := json.Unmarshal(response.Source, &retired); err != nil {
		t.Fatalf("decode retired document: %v", err)
	}
	if string(retired["retired"]) != "true" || len(retired["page_text"]) != 0 || len(retired["page_text_semantic_info"]) != 0 {
		t.Fatal("the delayed write restored deleted text")
	}
	var generation int64
	if err := json.Unmarshal(retired["search_generation"], &generation); err != nil || generation != deletion.Generation {
		t.Fatalf("retired generation = %d err %v, want %d", generation, err, deletion.Generation)
	}
}
