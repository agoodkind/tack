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
	// pitLimitSessions counts the search sessions the test opens before checking rejection at the limit.
	// The test sets the limit to the initial open context count plus pitLimitSessions.
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

// TestSearchPointInTimeLimitReturnsUnavailable checks tack_search at the point-in-time context limit.
// The test uses the public MCP tool with real FoundationDB and OpenSearch.
// It indexes the matching nodes, reads the initial open context count from OpenSearch node stats,
// and sets search.max_open_pit_context to that count plus pitLimitSessions.
// It opens searches that each return a cursor and keep their point in time open, then requires
// the open context count to equal the limit.
// It requires the next new search to return pitLimitUnavailableText instead of an unexpected
// server error, then requires a direct point-in-time request to fail with HTTP 429.
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

	limit := openPointInTimeContexts(t, fixture) + pitLimitSessions
	t.Cleanup(func() {
		if err := setPointInTimeLimit(context.WithoutCancel(integration.WithTestOperation(t, t.Context())), fixture, "null"); err != nil {
			t.Errorf("cleanup could not restore the default point-in-time limit: %v", err)
		}
	})
	if err := setPointInTimeLimit(integration.WithTestOperation(t, t.Context()), fixture, strconv.Itoa(limit)); err != nil {
		t.Fatalf("test could not set the point-in-time limit to %d: %v", limit, err)
	}

	for number := range pitLimitSessions {
		page, err := integration.TrySearch(fixture.Harness, pitLimitQuery, "")
		if err != nil || page.Cursor == "" {
			t.Fatalf("search session %d did not return a successful page with a cursor: result count %d, cursor %q, error %v", number+1, len(page.IDs), page.Cursor, err)
		}
	}
	if open := openPointInTimeContexts(t, fixture); open != limit {
		t.Fatalf("open point-in-time context count %d does not equal the limit %d", open, limit)
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

// openPointInTimeContexts sums point_in_time_current across all OpenSearch nodes.
// OpenSearch counts one context per shard that a point in time opens, the unit that
// search.max_open_pit_context limits.
func openPointInTimeContexts(t *testing.T, fixture integration.QueryFixture) int {
	t.Helper()
	stats, err := fixture.Client.Nodes.Stats(integration.WithTestOperation(t, t.Context()), &opensearchapi.NodesStatsReq{
		IndexMetric: []string{"search"}, Metric: []string{"indices"}, NodeID: nil, Header: nil,
		Params: opensearchapi.NodesStatsParams{},
	})
	if err != nil || len(stats.Nodes) == 0 {
		t.Fatalf("node stats request failed or did not return any OpenSearch nodes: %v", err)
	}
	total := 0
	for _, node := range stats.Nodes {
		total += node.Indices.Search.PointInTimeCurrent
	}
	return total
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
