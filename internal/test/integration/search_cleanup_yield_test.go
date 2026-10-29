package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

const (
	// timingPageBytes keeps each page small. A node with a few hundred bytes
	// of text has several pages.
	timingPageBytes = 128
	// cleanupBatchDocuments is the maximum number of old page documents that
	// one cleanup slice retires. A later cleanup slice retires any remaining
	// documents.
	cleanupBatchDocuments = 100
	// claimWait bounds the wait for released work to become claimable again
	// after its retry delay.
	claimWait = 30 * time.Second
	// drainSlices sets the maximum number of claims per drainWorkClass call.
	drainSlices = 1000
	// shortTimingText fits on one page at timingPageBytes.
	shortTimingText = "short timing text"
)

// claimClassOf claims work of class until it claims the work of nodeID, and
// yields every other claim. It retries until released work passes its
// retry delay.
func claimClassOf(t *testing.T, store *fdbadapter.SearchWorkStore, class searchdomain.WorkClass, nodeID uuid.UUID) searchdomain.Work {
	t.Helper()
	for deadline := time.Now().Add(claimWait); time.Now().Before(deadline); {
		work, claimed, err := store.Claim(t.Context(), class, "timing-inspector", time.Minute)
		if err != nil {
			t.Fatalf("claim %s work of %s: %v", class, nodeID, err)
		}
		if !claimed {
			time.Sleep(250 * time.Millisecond)
			continue
		}
		if work.NodeID == nodeID {
			return work
		}
		if err := store.Yield(t.Context(), work); err != nil {
			t.Fatalf("yield %s work of %s: %v", class, work.NodeID, err)
		}
	}
	t.Fatalf("%s work of %s was not claimable within %s", class, nodeID, claimWait)
	return searchdomain.Work{}
}

// drainWorkClass claims work of one class and runs worker.Process on each
// claim. It stops when Claim reports that it did not lease an item. It
// fails the test if the class still has work after the claim limit.
func drainWorkClass(t *testing.T, store *fdbadapter.SearchWorkStore, worker *service.SearchWorker, class searchdomain.WorkClass) {
	t.Helper()
	for range drainSlices {
		work, claimed, err := store.Claim(t.Context(), class, "class-driver", time.Minute)
		if err != nil {
			t.Fatalf("claim %s work: %v", class, err)
		}
		if !claimed {
			return
		}
		if err := worker.Process(t.Context(), work); err != nil {
			t.Fatalf("process %s work of %s: %v", class, work.NodeID, err)
		}
	}
	t.Fatalf("%s work did not drain within %d slices", class, drainSlices)
}

// requireIndexedNode compares active OpenSearch documents with pages from
// the production content reader for the node's current revision. It fails
// the test unless the counts match and each document matches the reader
// page at the same position in all of these fields:
//   - The text matches.
//   - The page ordinal matches.
//   - The node revision matches.
//   - The projection version matches.
func requireIndexedNode(t *testing.T, env *migrationEnv, nodeID uuid.UUID) {
	t.Helper()
	if mismatch := indexMismatch(t, env, nodeID); mismatch != "" {
		t.Fatal(mismatch)
	}
}

// indexMismatch returns the first difference that requireIndexedNode
// reports, or an empty string when the active documents match the pages.
func indexMismatch(t *testing.T, env *migrationEnv, nodeID uuid.UUID) string {
	t.Helper()
	documents := env.pages(t, nodeID, false)
	pages := env.readerPages(t, nodeID)
	if len(documents) != len(pages) {
		return fmt.Sprintf("node %s has %d active documents, want %d pages", nodeID, len(documents), len(pages))
	}
	for number, document := range documents {
		page := pages[number]
		textMatches := document.PageText != nil && *document.PageText == page.Text
		if !textMatches || document.PageOrdinal != page.Ordinal || document.NodeRevision != page.Revision ||
			document.ProjectionVersion != page.ProjectionVersion {
			return fmt.Sprintf("document %d of node %s does not match reader page %d", number, nodeID, page.Ordinal)
		}
	}
	return ""
}

// TestSearchCleanupYieldsAfter100Documents indexes a node with more than 100
// pages, shortens it to one page, and runs its cleanup one slice at a time.
// The first slice must retire exactly 100 old documents and leave the
// cleanup work claimable. Later slices retire the rest.
func TestSearchCleanupYieldsAfter100Documents(t *testing.T) {
	env := newMigrationEnv(t, timingPageBytes)
	nodeID := uuid.Must(uuid.NewV7())
	env.putNode(t, nodeID, strings.Repeat("cleanup yield text ", 700))
	env.runIdle(t)
	original := env.pages(t, nodeID, false)
	if len(original) <= cleanupBatchDocuments {
		t.Fatalf("node indexed %d pages, want more than %d", len(original), cleanupBatchDocuments)
	}

	env.putNode(t, nodeID, shortTimingText)
	work := env.Stores.SearchWork(clock.Wall{})
	drainWorkClass(t, work, env.Worker, searchdomain.WorkClassLive)
	before := len(env.pages(t, nodeID, true))
	if err := env.Worker.Process(t.Context(), claimClassOf(t, work, searchdomain.WorkClassCleanup, nodeID)); err != nil {
		t.Fatalf("process the first cleanup slice: %v", err)
	}
	if retired := len(env.pages(t, nodeID, true)) - before; retired != cleanupBatchDocuments {
		t.Fatalf("the first cleanup slice retired %d documents, want %d", retired, cleanupBatchDocuments)
	}
	if err := env.Worker.Process(t.Context(), claimClassOf(t, work, searchdomain.WorkClassCleanup, nodeID)); err != nil {
		t.Fatalf("process the second cleanup slice: %v", err)
	}
	drainWorkClass(t, work, env.Worker, searchdomain.WorkClassCleanup)

	// before counts the pages retired before the first cleanup slice.
	// Indexing retired those pages before the node was shortened, and the
	// check excludes them.
	if retired := len(env.pages(t, nodeID, true)) - before; retired != len(original) {
		t.Fatalf("cleanup retired %d documents, want the %d documents of the old revision", retired, len(original))
	}
	requireIndexedNode(t, env, nodeID)
}
