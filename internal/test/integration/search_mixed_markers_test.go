package integration_test

import (
	"net/http"
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// searchMixedResultPageSize is the number of results on the first tack_search page.
	searchMixedResultPageSize = 25
	// searchMixedMarkersCallTimeout limits each public HTTP call to two minutes.
	searchMixedMarkersCallTimeout = 2 * time.Minute
)

// TestSearchMixedMarkersBeyondFirstPage checks that each marker query finds its issue on the first
// tack_search result page when the run creates more marker issues than 25 times the tenant count.
// It calls datagen.RunSearchMixed over HTTP against two runtime graphs with real FoundationDB,
// YugabyteDB, and OpenSearch. ProbeAfterCatchUp prevents marker settlement during writes.
func TestSearchMixedMarkersBeyondFirstPage(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	endpoints := startSearchMixedProcesses(t, cfg)
	ctx := integration.WithTestOperation(t, t.Context())
	options := datagen.SearchMixedOptions{
		Seed: clock.Now().UnixNano(), Rate: 30, WriteRate: 480, Duration: 40 * time.Second, Concurrency: 4,
		Commit: true, BurstWrites: 10, CatchUp: 0, Endpoints: endpoints,
		Client: &http.Client{Timeout: searchMixedMarkersCallTimeout}, Rebuild: nil, MetadataWrites: 0, Moves: 0,
		ProbeAfterCatchUp: true,
	}
	summary, err := datagen.RunSearchMixed(ctx, ctx, cfg, options)
	if err != nil {
		t.Fatalf("qa datagen search-mixed: RunSearchMixed returned an error: %v (summary=%+v)", err, summary)
	}
	if summary.Tenants == 0 || summary.Creates <= searchMixedResultPageSize*summary.Tenants {
		t.Fatalf("qa datagen search-mixed: the run must create more marker issues than %d times the tenant count: %d creates, %d tenants", searchMixedResultPageSize, summary.Creates, summary.Tenants)
	}
	if summary.MarkersNotFound != 0 || summary.MarkersUnsearched != 0 || !summary.CatchUp.Completed {
		t.Fatalf("qa datagen search-mixed: catch-up must complete, and the final pass after catch-up must search every marker with its own tack_search query and find each issue on the first result page: markers_not_found=%d markers_unsearched=%d catch_up.completed=%t",
			summary.MarkersNotFound, summary.MarkersUnsearched, summary.CatchUp.Completed)
	}
	if err := summary.Verify(ctx, "none"); err != nil {
		t.Fatalf("qa datagen search-mixed: Verify returned an error: %v", err)
	}
}
