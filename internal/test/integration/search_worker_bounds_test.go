package integration

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/service"
)

const (
	// boundPageBytes keeps the bounded node small while it spans many pages.
	boundPageBytes = 128
	// boundSlicePages is the production page bound of one slice.
	boundSlicePages = 32
	// boundSmallMaxBytes is a valid byte bound near the encoded size of three
	// small pages.
	boundSmallMaxBytes = 2000
)

// boundSettings returns the worker settings with a slice time and a lease
// long enough that the page or byte bound, not the two-second slice budget,
// stops a slice while the model encodes each page.
func boundSettings(settings config.SearchWorkerSettings) config.SearchWorkerSettings {
	settings.SliceBudget = 10 * time.Minute
	settings.Lease = 20 * time.Minute
	return settings
}

// runFirstClaimedSlice runs slices until one claims work.
func runFirstClaimedSlice(t *testing.T, worker *service.SearchWorker) {
	t.Helper()
	for range 20 {
		claimed, err := worker.RunSlice(t.Context())
		if err != nil {
			t.Fatalf("run slice: %v", err)
		}
		if claimed {
			return
		}
	}
	t.Fatal("no slice claimed work within 20 attempts")
}

// TestSearchWorkerSliceStopsAtPageBound requires the first slice of a node
// with more than 32 pages to index pages 0 to 31 and stop with work pending.
// Page 0 is in OpenSearch before the slice reads the final page. Later slices
// index the remaining pages once each.
func TestSearchWorkerSliceStopsAtPageBound(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	fixture := putSearchText(t, stores, strings.Repeat("bounded slice text ", 220), readerExcludedValue)
	pages := readSearchPages(t, stores, fixture.NodeID, boundPageBytes)
	if len(pages) <= boundSlicePages {
		t.Fatalf("fixture has %d pages, want more than %d", len(pages), boundSlicePages)
	}
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, boundSettings(searchWorkerSettings(boundPageBytes)))
	runFirstClaimedSlice(t, worker)
	written := searchNodePages(t, client, index, fixture.NodeID, false)
	if len(written) != boundSlicePages {
		t.Fatalf("first slice indexed %d of %d pages, want the page bound %d", len(written), len(pages), boundSlicePages)
	}
	for ordinal, document := range written {
		if document.PageOrdinal != uint64(ordinal) {
			t.Fatalf("first slice document %d has ordinal %d, want pages 0 to %d in order", ordinal, document.PageOrdinal, boundSlicePages-1)
		}
	}
	runSearchWorkerUntilIdle(t, worker)
	requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), readSearchPages(t, stores, fixture.NodeID, boundPageBytes), 0)
}

// TestSearchWorkerSliceStopsAtByteBound requires a slice to stop after the
// first page write that brings its encoded bytes to the byte bound, with work
// pending. Later slices index the remaining pages once each.
func TestSearchWorkerSliceStopsAtByteBound(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, meter, index := newMeteredSearchIndex(t, stores)
	fixture := putSearchText(t, stores, strings.Repeat("bounded slice text ", 220), readerExcludedValue)
	pages := readSearchPages(t, stores, fixture.NodeID, boundPageBytes)
	settings := boundSettings(searchWorkerSettings(boundPageBytes))
	settings.MaxBytes = boundSmallMaxBytes
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, settings)
	runFirstClaimedSlice(t, worker)
	bodies := meter.take()
	total := 0
	for number, body := range bodies {
		if total >= settings.MaxBytes {
			t.Fatalf("slice sent bulk request %d after %d bytes, at or above the bound %d", number, total, settings.MaxBytes)
		}
		total += body.bytes
	}
	if total < settings.MaxBytes || len(bodies) >= len(pages) || len(bodies) >= boundSlicePages {
		t.Fatalf("slice sent %d requests with %d bytes for %d pages, want a stop at the byte bound %d before the last page", len(bodies), total, len(pages), settings.MaxBytes)
	}
	if written := searchNodePages(t, client, index, fixture.NodeID, false); len(written) != len(bodies) {
		t.Fatalf("first slice indexed %d pages, want %d", len(written), len(bodies))
	}
	runSearchWorkerUntilIdle(t, worker)
	requireIndexedPages(t, searchNodePages(t, client, index, fixture.NodeID, false), readSearchPages(t, stores, fixture.NodeID, boundPageBytes), 0)
}
