package integration_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// pitLimitSessions is both the search.max_open_pit_context limit and the number of search
	// sessions the test opens before the search that must fail. The test closes every open
	// point in time on the engine before setting the limit.
	pitLimitSessions = 2
	// pitLimitCorpusNodes is the matching node count, which exceeds the first page's limit of 25 results.
	// Each first page returns a cursor, and its session keeps its point in time open.
	pitLimitCorpusNodes = 30
	// pitLimitDrainSlices bounds the worker slices for indexing the corpus and bootstrapped nodes.
	// It uses the existing integration helper's bound: ten slices per corpus node plus 100 for the
	// fixture's bootstrapped nodes.
	pitLimitDrainSlices = 10*pitLimitCorpusNodes + 100
	// pitLimitQuery is the text that every corpus node contains and every test search uses.
	pitLimitQuery = "cobalt tideline ledger"
	// pitLimitUnavailableText is the public tack_search response for an unavailable engine.
	pitLimitUnavailableText = "Search is temporarily unavailable."
)

// TestSearchPointInTimeLimitReturnsUnavailable checks limit exhaustion through the public
// tack_search MCP tool with real FoundationDB and OpenSearch. It indexes the matching nodes,
// then closes every point in time that earlier tests left open on the shared engine.
// It sets search.max_open_pit_context to pitLimitSessions and opens pitLimitSessions searches.
// Each search returns a cursor and keeps its point in time open.
// The test requires the serving index to report pitLimitSessions open point-in-time contexts.
// It requires the next new search to return pitLimitUnavailableText instead of an unexpected
// server error. It then requires a direct point-in-time request to fail with HTTP 429.
// Cleanup restores the default limit for later tests in the same process.
func TestSearchPointInTimeLimitReturnsUnavailable(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	entryID := integration.EntryPoint(t, fixture, workspace)
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	for number := range pitLimitCorpusNodes {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Limit node %d", number), pitLimitQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, pitLimitDrainSlices)

	closeOpenPointsInTime(t, fixture)
	t.Cleanup(func() {
		if err := setPointInTimeLimit(context.WithoutCancel(integration.WithTestOperation(t, t.Context())), fixture, "null"); err != nil {
			t.Errorf("cleanup could not restore the default point-in-time limit: %v", err)
		}
	})
	if err := setPointInTimeLimit(integration.WithTestOperation(t, t.Context()), fixture, strconv.Itoa(pitLimitSessions)); err != nil {
		t.Fatalf("test could not set the point-in-time limit to %d: %v", pitLimitSessions, err)
	}

	for number := range pitLimitSessions {
		page, err := integration.TrySearch(fixture.Harness, pitLimitQuery, "")
		if err != nil || page.Cursor == "" {
			t.Fatalf("search session %d did not return a successful page with a cursor: result count %d, cursor %q, error %v", number+1, len(page.IDs), page.Cursor, err)
		}
	}
	if open := servingIndexPointInTimeContexts(t, fixture); open != pitLimitSessions {
		t.Fatalf("open point-in-time context count %d does not equal the limit %d", open, pitLimitSessions)
	}

	_, err := integration.TrySearch(fixture.Harness, pitLimitQuery, "")
	if err == nil || err.Error() != pitLimitUnavailableText {
		t.Fatalf("search at the point-in-time limit returned error %v; the test requires %q", err, pitLimitUnavailableText)
	}
	requireDirectPointInTimeRejected(t, fixture)
}

// requireDirectPointInTimeRejected uses the OpenSearch client to request a point in time on the
// fixture's serving index and fails the test unless OpenSearch returns HTTP 429.
// This check confirms that the context limit produces the status that engineCause classifies.
func requireDirectPointInTimeRejected(t *testing.T, fixture integration.QueryFixture) {
	t.Helper()
	created, err := fixture.Client.PointInTime.Create(integration.WithTestOperation(t, t.Context()), opensearchapi.PointInTimeCreateReq{
		Indices: []string{fixture.Index}, Header: nil, Params: opensearchapi.PointInTimeCreateParams{KeepAlive: time.Minute},
	})
	status := 0
	if created.Inspect().Response != nil {
		status = created.Inspect().Response.StatusCode
	}
	if err == nil || status != http.StatusTooManyRequests {
		t.Fatalf("direct point-in-time request returned HTTP %d with error %v; the test requires HTTP %d", status, err, http.StatusTooManyRequests)
	}
}

// closeOpenPointsInTime lists every open point in time on the engine and deletes them.
// Earlier tests in the same process can leave sessions with open points in time.
// OpenSearch counts those contexts against search.max_open_pit_context.
// After this function returns, the engine does not have any open point-in-time contexts.
func closeOpenPointsInTime(t *testing.T, fixture integration.QueryFixture) {
	t.Helper()
	listed, err := fixture.Client.PointInTime.Get(integration.WithTestOperation(t, t.Context()), &opensearchapi.PointInTimeGetReq{
		Header: nil, Params: opensearchapi.PointInTimeGetParams{},
	})
	if err != nil {
		t.Fatalf("listing open points in time failed: %v", err)
	}
	identifiers := make([]string, 0, len(listed.Pits))
	for _, pit := range listed.Pits {
		identifiers = append(identifiers, pit.PitID)
	}
	if len(identifiers) == 0 {
		return
	}
	_, err = fixture.Client.PointInTime.Delete(integration.WithTestOperation(t, t.Context()), opensearchapi.PointInTimeDeleteReq{
		PitID: identifiers, Header: nil, Params: opensearchapi.PointInTimeDeleteParams{},
	})
	if err != nil {
		t.Fatalf("deleting %d open points in time failed: %v", len(identifiers), err)
	}
}

// servingIndexPointInTimeContexts returns point_in_time_current from the index stats
// of the fixture's serving index. These stats count the contexts of that index's live shards.
// The test does not read node stats. Node stats also add point_in_time_current from each
// shard that closed while a point in time was open, including shards of indexes that earlier
// tests deleted.
func servingIndexPointInTimeContexts(t *testing.T, fixture integration.QueryFixture) int {
	t.Helper()
	stats, err := fixture.Client.Indices.Stats(integration.WithTestOperation(t, t.Context()), &opensearchapi.IndicesStatsReq{
		Indices: []string{fixture.Index}, Metrics: []string{"search"}, Header: nil, Params: opensearchapi.IndicesStatsParams{},
	})
	if err != nil {
		t.Fatalf("reading index stats for %s failed: %v", fixture.Index, err)
	}
	return stats.All.Total.Search.PointInTimeCurrent
}

// setPointInTimeLimit writes search.max_open_pit_context as a persistent cluster setting.
// A value of null restores the default.
func setPointInTimeLimit(ctx context.Context, fixture integration.QueryFixture, value string) error {
	body := `{"persistent":{"search.max_open_pit_context":` + value + `}}`
	response, err := fixture.Client.Cluster.PutSettings(ctx, opensearchapi.ClusterPutSettingsReq{
		Body: strings.NewReader(body), Header: nil, Params: opensearchapi.ClusterPutSettingsParams{},
	})
	if err != nil {
		return fmt.Errorf("request to set persistent search.max_open_pit_context failed: %w", err)
	}
	if !response.Acknowledged {
		return fmt.Errorf("cluster settings response did not acknowledge search.max_open_pit_context value %s", value)
	}
	return nil
}
