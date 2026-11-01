package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v5"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"

	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// The default monitor checks disk usage every 30 seconds and can defer
// clearing high-watermark state for the 60-second reroute interval.
const createIndexBlockWait = 2 * time.Minute

func TestSearchCreateIndexBlockProbe(t *testing.T) {
	engine := testenv.OpenSearch(t)
	client := integration.OpenSearchClient(t, engine)
	if err := testenv.CheckOpenSearchDiskReadiness(t.Context(), engine); err != nil {
		t.Fatalf("the fresh engine failed disk readiness: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var filesystem json.RawMessage
	response, err := opensearch.Execute(ctx, client.Client, http.MethodGet, &opensearchapi.NodesStatsReq{
		Metric: []string{"fs"}, Params: &opensearchapi.NodesStatsParams{DebugParams: opensearchapi.DebugParams{FilterPath: []string{"nodes.*.fs.total"}}},
	}, &filesystem)
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("read the engine filesystem: %v", err)
	}
	t.Logf("engine filesystem: %s", filesystem)
	t.Cleanup(func() { waitForCreateIndexBlock(t, client, false) })
	// Cleanup clears all three watermark overrides and the info interval
	// override in one request. OpenSearch rejects updates that mix percentage
	// watermarks with the byte values from the node defaults.
	t.Cleanup(func() {
		clearPersistentSettings(t, client, watermarkLowKey, watermarkHighKey, watermarkFloodKey, "cluster.info.update.interval")
	})
	settings := `{"persistent":{"cluster.routing.allocation.disk.watermark.low":"0%","cluster.routing.allocation.disk.watermark.high":"0%","cluster.routing.allocation.disk.watermark.flood_stage":"100%","cluster.info.update.interval":"10s"}}`
	if _, err := client.Cluster.PutSettings(ctx, &opensearchapi.ClusterPutSettingsReq{BodyReader: strings.NewReader(settings)}); err != nil {
		t.Fatalf("set the probe watermarks: %v", err)
	}
	waitForCreateIndexBlock(t, client, true)
	var health json.RawMessage
	response, err = opensearch.Execute(ctx, client.Client, http.MethodGet, &opensearchapi.ClusterHealthReq{}, &health)
	if err != nil || response == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("read the blocked cluster's health: %v", err)
	}
	t.Logf("blocked engine health: HTTP %d %s", response.StatusCode, health)
	_, err = client.Indices.Create(ctx, opensearchapi.IndicesCreateReq{Index: "tack-block-probe-" + uuid.NewString()})
	var blocked *opensearch.StructError
	if !errors.As(err, &blocked) || blocked.Status != http.StatusForbidden || blocked.Err.Type != "index_create_block_exception" || !strings.Contains(blocked.Err.Reason, "FORBIDDEN/10/cluster create-index blocked (api)") {
		t.Fatalf("create index under the automatic block returned %v", err)
	}
	t.Logf("automatic refusal: %v", err)
	readinessErr := testenv.CheckOpenSearchDiskReadiness(ctx, engine)
	if readinessErr == nil || !strings.Contains(readinessErr.Error(), "OpenSearch fixture blocks index creation") ||
		!strings.Contains(readinessErr.Error(), "Putting index create block on cluster") {
		t.Fatalf("the blocked engine passed readiness or omitted the monitor evidence: %v", readinessErr)
	}
	t.Logf("blocked readiness: %v", readinessErr)
}

func waitForCreateIndexBlock(t *testing.T, client *opensearchapi.Client, blocked bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), createIndexBlockWait)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		settings, err := client.Cluster.GetSettings(ctx, &opensearchapi.ClusterGetSettingsReq{Params: &opensearchapi.ClusterGetSettingsParams{FlatSettings: new(true)}})
		if err != nil {
			t.Fatalf("read the automatic create-index block: %v", err)
		}
		value := settings.Persistent["cluster.blocks.create_index"]
		if (string(value) == `"true"`) == blocked {
			t.Logf("automatic persistent block: %s, want %t", value, blocked)
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("wait for the automatic create-index block %t: %v", blocked, ctx.Err())
		case <-ticker.C:
		}
	}
}
