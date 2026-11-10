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

// The disk size and fill targets test free space above and below the
// shared engine's floor while usage exceeds the default high watermark.
const (
	floorMemoryBytes int64 = 8 << 30
	floorDataBytes   int64 = 16 << 30
	floorBytes       int64 = 1 << 30
	// floorAbovePercent targets free space above the 1gb floor while disk
	// usage exceeds the default high watermark of 90 percent.
	floorAbovePercent = 92
	// floorBelowPercent targets free space below the 1gb floor.
	floorBelowPercent   = 95
	floorDefaultHigh    = 90
	floorQuietPeriod    = time.Minute
	watermarkLowKey     = "cluster.routing.allocation.disk.watermark.low"
	watermarkHighKey    = "cluster.routing.allocation.disk.watermark.high"
	watermarkFloodKey   = "cluster.routing.allocation.disk.watermark.flood_stage"
	createIndexBlockKey = "cluster.blocks.create_index"
	floorIndexPrefix    = "tack-floor-"
	floorBlockType      = "index_create_block_exception"
	floorBlockReason    = "FORBIDDEN/10/cluster create-index blocked (api)"
	floorPollInterval   = time.Second
	floorRequestBounds  = time.Minute
)

// TestSearchClusterSharedDiskFloor checks index creation on one disposable
// engine with the shared disk policy. Above the floor, it requires an unset
// block and successful creation. Percentage overrides must block creation
// at the same usage. Clearing all three overrides must release the block
// and allow creation. Below the floor, the engine must block creation.
// Removing the filler must trigger automatic release and allow creation.
// The test requires both TACK_SEARCH_INTEGRATION=1 and TACK_SEARCH_CLUSTER=1.
func TestSearchClusterSharedDiskFloor(t *testing.T) {
	engine := testenv.DisposableOpenSearch(
		t,
		testenv.DisposableOpenSearchOptions{
			MemoryBytes:      floorMemoryBytes,
			DataBytes:        floorDataBytes,
			SharedDiskPolicy: true,
		},
	)
	client := integration.OpenSearchClient(t, engine.Fixture)

	size, used := engine.FillData(t, floorAbovePercent)
	t.Logf("data path after the first fill: %d of %d bytes used", used, size)
	if size-used <= floorBytes || used*100 <= size*floorDefaultHigh {
		t.Fatalf(
			"data path has %d of %d bytes used, want free space above the floor and usage above %d percent",
			used,
			size,
			floorDefaultHigh,
		)
	}

	requireNoCreateIndexBlock(t, client, floorQuietPeriod)
	requireIndexCreated(t, client)

	watermarks := []string{watermarkLowKey, watermarkHighKey, watermarkFloodKey}
	t.Cleanup(func() { clearPersistentSettings(t, client, watermarks...) })
	putPersistentSettings(
		t,
		client,
		map[string]string{
			watermarkLowKey:   `"85%"`,
			watermarkHighKey:  `"90%"`,
			watermarkFloodKey: `"95%"`,
		},
	)
	waitForCreateIndexBlock(t, client, true)
	requireIndexRefused(t, client)
	clearPersistentSettings(t, client, watermarks...)
	waitForCreateIndexBlock(t, client, false)
	requireIndexCreated(t, client)

	size, used = engine.FillData(t, floorBelowPercent)
	t.Logf("data path after the second fill: %d of %d bytes used", used, size)
	if size-used >= floorBytes {
		t.Fatalf("data path has %d of %d bytes used, want free space below the floor", used, size)
	}

	waitForCreateIndexBlock(t, client, true)
	requireIndexRefused(t, client)

	size, used = engine.FreeData(t)
	t.Logf("data path after the free: %d of %d bytes used", used, size)
	waitForCreateIndexBlock(t, client, false)
	requireIndexCreated(t, client)
}

// requireNoCreateIndexBlock checks the index creation block throughout period.
// It fails the test if a read fails, the engine sets the block, or the test
// context cancels. It returns only after period expires with at least one
// successful read.
func requireNoCreateIndexBlock(t *testing.T, client *opensearchapi.Client, period time.Duration) {
	t.Helper()
	expiry := time.NewTimer(period)
	defer expiry.Stop()
	ticker := time.NewTicker(floorPollInterval)
	defer ticker.Stop()
	reads := 0

	for {
		ctx, cancel := context.WithTimeout(t.Context(), floorRequestBounds)
		settings, err := client.Cluster.GetSettings(
			ctx,
			&opensearchapi.ClusterGetSettingsReq{
				Params: &opensearchapi.ClusterGetSettingsParams{FlatSettings: new(true)},
			},
		)
		cancel()
		if err != nil {
			t.Fatalf("read cluster.blocks.create_index after %d successful reads: %v", reads, err)
		}

		reads++
		if string(settings.Persistent[createIndexBlockKey]) == `"true"` {
			t.Fatalf(
				"the engine set %s after %d reads with free space above the floor",
				createIndexBlockKey,
				reads,
			)
		}

		select {
		case <-t.Context().Done():
			t.Fatalf(
				"observe %s after %d successful reads: %v",
				createIndexBlockKey,
				reads,
				context.Cause(t.Context()),
			)
		case <-expiry.C:
			t.Logf("%s stayed unset for %d reads over %s", createIndexBlockKey, reads, period)
			return
		case <-ticker.C:
		}
	}
}

// requireIndexCreated requires successful creation of a unique index
// and registers cleanup to delete it when the test ends.
// Cleanup reports any delete error with t.Errorf and continues.
func requireIndexCreated(t *testing.T, client *opensearchapi.Client) {
	t.Helper()
	name := floorIndexPrefix + uuid.NewString()
	ctx, cancel := context.WithTimeout(t.Context(), floorRequestBounds)
	defer cancel()
	if _, err := client.Indices.Create(
		ctx,
		opensearchapi.IndicesCreateReq{Index: name},
	); err != nil {
		t.Fatalf("create index %s: %v", name, err)
	}

	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), floorRequestBounds)
		defer stop()
		if _, err := client.Indices.Delete(
			cleanup,
			&opensearchapi.IndicesDeleteReq{Indices: []string{name}},
		); err != nil {
			t.Errorf("delete index %s: %v", name, err)
		}
	})
}

// requireIndexRefused attempts to create a unique index and requires
// HTTP 403 with index_create_block_exception and the expected block reason.
func requireIndexRefused(t *testing.T, client *opensearchapi.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), floorRequestBounds)
	defer cancel()
	_, err := client.Indices.Create(
		ctx,
		opensearchapi.IndicesCreateReq{Index: floorIndexPrefix + uuid.NewString()},
	)
	var blocked *opensearch.StructError
	if !errors.As(err, &blocked) || blocked.Status != http.StatusForbidden ||
		blocked.Err.Type != floorBlockType ||
		!strings.Contains(blocked.Err.Reason, floorBlockReason) {
		t.Fatalf("create index under the block returned %v", err)
	}
}

// putPersistentSettings updates persistent cluster settings in one request
// and requires acknowledgment. Each value contains JSON; null removes
// the corresponding override. The request can run during test cleanup
// after the test context cancels.
func putPersistentSettings(t *testing.T, client *opensearchapi.Client, settings map[string]string) {
	t.Helper()

	values := make(map[string]json.RawMessage, len(settings))
	for key, value := range settings {
		values[key] = json.RawMessage(value)
	}

	body, err := json.Marshal(map[string]map[string]json.RawMessage{"persistent": values})
	if err != nil {
		t.Fatalf("encode persistent settings: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), floorRequestBounds)
	defer cancel()
	response, err := client.Cluster.PutSettings(
		ctx,
		&opensearchapi.ClusterPutSettingsReq{BodyReader: strings.NewReader(string(body))},
	)
	if err != nil || !response.Acknowledged {
		t.Fatalf("set persistent settings %s: %v", body, err)
	}
}

// clearPersistentSettings removes the persistent overrides for all supplied
// keys in one request.
func clearPersistentSettings(t *testing.T, client *opensearchapi.Client, keys ...string) {
	t.Helper()

	cleared := make(map[string]string, len(keys))
	for _, key := range keys {
		cleared[key] = "null"
	}

	putPersistentSettings(t, client, cleared)
}
