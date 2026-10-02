package integration

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"goodkind.io/tack/internal/domain/node"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

const (
	runtimePageBytes = 128
	// maxStaleCursorRetries bounds the polls of
	// TestSearchRuntimeIndexesThroughGraph that end with ErrContentChanged.
	maxStaleCursorRetries = 10
)

// configureSearchRuntime sets the production search environment for the
// shared test engine.
func configureSearchRuntime(t *testing.T) {
	t.Helper()
	engine := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
	caPath := filepath.Join(t.TempDir(), "opensearch-ca.pem")
	if err := os.WriteFile(caPath, []byte(engine.CA), 0o600); err != nil {
		t.Fatalf("write OpenSearch CA: %v", err)
	}
	secret := engine.Password
	t.Setenv("OPENSEARCH_ENDPOINT", engine.Endpoint)
	t.Setenv("OPENSEARCH_CA", caPath)
	t.Setenv("OPENSEARCH_USERNAME", engine.Username)
	t.Setenv("OPENSEARCH_PASSWORD", secret)
	t.Setenv("OPENSEARCH_PAGE_BYTES", strconv.Itoa(runtimePageBytes))
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "50ms")
	t.Setenv("OPENSEARCH_SHARDS", "1")
	t.Setenv("OPENSEARCH_ROUTING_SHARDS", "24")
	t.Setenv("OPENSEARCH_REPLICAS", "0")
}

// currentRevisionIndexed reports whether the document count equals the
// reader page count and each document revision equals the reader revision.
func currentRevisionIndexed(documents []searchPageSource, pages []node.ContentPage) bool {
	if len(documents) != len(pages) {
		return false
	}
	for _, document := range documents {
		if document.NodeRevision != pages[0].Revision {
			return false
		}
	}
	return true
}

// TestSearchRuntimeIndexesThroughGraph requires the worker loops of the
// production graph to index every page of a stored node within two minutes.
func TestSearchRuntimeIndexesThroughGraph(t *testing.T) {
	stores := newSearchStore(t)
	_, client, _, index := newSearchIndex(t, stores)
	configureSearchRuntime(t)
	cfg := queryConfig(t)
	graph, err := appruntime.BuildGraph(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build graph: %v", err)
	}
	t.Cleanup(graph.Close)
	graph.StartSearchWorkers(t.Context())

	fixture := putSearchText(t, stores, readerIncludedValue(), readerExcludedValue)
	started := time.Now()
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	staleReads := 0
	for {
		// The fixture's property definition and node type writes request a
		// rescan. A worker that runs the rescan after the node write schedules
		// a new revision of the node. The reader rejects a continuation cursor
		// of the earlier revision with ErrContentChanged, and the next poll
		// reads the new revision from its first page.
		pages, err := readSearchPageSequence(t, stores, fixture.NodeID, runtimePageBytes)
		switch {
		case errors.Is(err, node.ErrContentChanged):
			staleReads++
			if staleReads > maxStaleCursorRetries {
				t.Fatalf("page reads returned ErrContentChanged %d times in %s: %v", staleReads, time.Since(started), err)
			}
		case err != nil:
			t.Fatal(err)
		}
		documents := searchNodePages(t, client, index, fixture.NodeID, false)
		if err == nil && currentRevisionIndexed(documents, pages) {
			requireIndexedPages(t, documents, pages, 1)
			return
		}
		poll := time.NewTimer(500 * time.Millisecond)
		select {
		case <-deadline.C:
			poll.Stop()
			t.Fatalf("the runtime workers indexed %d of %d pages", len(documents), len(pages))
		case <-poll.C:
		}
	}
}
