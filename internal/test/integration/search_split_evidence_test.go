package integration

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// splitFaultRequest splits the serving index from one primary shard to two.
var splitFaultRequest = searchdomain.BeginRebuild{
	Mode: searchdomain.ReplacementSplit, PrimaryShards: 2, RoutingShards: 24, Replicas: 0, Restored: false, Reason: "test",
}

// TestSearchSplitRecoversFromStepFailures fails the steps of a split in turn
// with real engine conditions. First the read-only source refuses the write
// block. Then the cluster shard limit refuses the split target. Each of these
// failures ends its replacement, and recovery must restore source writes,
// keep the public alias on the source, delete the failed target, and run
// from the durable failed state in a new worker. A third split then runs
// [failReplayValidationSwitch], which fails replay of the mutations that the
// split pause queued, validation, and alias switching. That split must
// resume after each condition clears and serve every current node.
func TestSearchSplitRecoversFromStepFailures(t *testing.T) {
	shortenSearchSessions(t)
	fixture := newQueryFixture(t, defaultQueryOptions())
	fixture.Worker = faultWorker(t, fixture)
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	steady := putOpaqueNode(t, fixture, kind, entry, "steady split fault node", "sienna split steady", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	source := fixture.Index

	setIndexBlock(t, fixture.Client, source, readOnlySetting, true)
	blocked := beginRebuild(t, fixture, splitFaultRequest)
	failed := runFaultedRebuildUntil(t, fixture, source, func(current searchdomain.Rebuild, found bool, failures []error) bool {
		return found && current.State == searchdomain.RebuildFailed && failedWith(failures, "restore writes to index "+source)
	})
	requireFailure(t, failed, "set write block true on "+source)
	requireServing(t, fixture, source, "")
	requireSearchFinds(t, fixture, "sienna split", steady)
	during := putOpaqueNode(t, fixture, kind, entry, "during split fault node", "sienna split during", readerExcludedValue)
	clearIndexBlock(t, fixture.Client, source, readOnlySetting)
	requireRecovered(t, fixture, source, blocked.TargetIndex)
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, source, []uuid.UUID{during})

	refused := beginRebuild(t, fixture, splitFaultRequest)
	runBoundedRebuildUntil(t, fixture, source, func(current searchdomain.Rebuild, found bool) bool { return found && current.Paused })
	setShardLimit(t, fixture, "1")
	failed = runBoundedRebuildUntil(t, fixture, source, inState(searchdomain.RebuildFailed))
	requireFailure(t, failed, "split "+source+" into "+refused.TargetIndex)
	if !writeBlocked(t, fixture.Client, source) {
		t.Fatalf("the failed split left source %s without its write block", source)
	}
	requireServing(t, fixture, source, "")
	setShardLimit(t, fixture, "null")
	requireRecovered(t, fixture, source, refused.TargetIndex)

	split := beginRebuild(t, fixture, splitFaultRequest)
	runBoundedRebuildUntil(t, fixture, source, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.Paused && current.State == searchdomain.RebuildCreating
	})
	queued := putOpaqueNode(t, fixture, kind, entry, "queued split fault node", "sienna split queued", readerExcludedValue)
	runBoundedRebuildUntil(t, fixture, source, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State != searchdomain.RebuildCreating
	})
	requireGreen(t, fixture.Client, split.TargetIndex)
	setIndexBlock(t, fixture.Client, split.TargetIndex, writeBlockSetting, true)
	requireServingAfterMirrorRefusal(t, fixture, queued, split.TargetIndex)
	failReplayValidationSwitch(t, fixture, split, queued, steady, "sienna split")
	requireServing(t, fixture, split.TargetIndex, source)
	if settings, err := fixture.Adapter.IndexSettings(WithTestOperation(t, t.Context()), split.TargetIndex); err != nil || settings.Primaries != 2 {
		t.Fatalf("split target primaries = %d, error = %v, want 2", settings.Primaries, err)
	}
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, split.TargetIndex, []uuid.UUID{steady, during, queued})
	requireSearchFinds(t, fixture, "sienna split", steady, during, queued)
}

// requireSplitRejected requires the split check to reject each primary
// count for source and requires the source to keep accepting writes.
func requireSplitRejected(t *testing.T, fixture queryFixture, source string, counts ...int) {
	t.Helper()
	for _, rejected := range counts {
		if _, err := fixture.Adapter.ValidateSplit(WithTestOperation(t, t.Context()), source, rejected); err == nil {
			t.Fatalf("split of %s to %d primary shards was accepted", source, rejected)
		}
		requireWritable(t, fixture.Client, source)
	}
}

func splitMapping(t *testing.T, fixture queryFixture, index string) string {
	t.Helper()
	mappingJSON := nativeMapping(t, fixture.Client, index)
	var mapping map[string]any
	if err := json.Unmarshal(mappingJSON, &mapping); err != nil {
		t.Fatalf("decode split mapping: %v", err)
	}
	encoded, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("encode split mapping: %v", err)
	}
	return string(encoded)
}

// fixedSearchFields are the top-level index fields that Tack defines, plus the
// chunk field that OpenSearch generates for page_text.
var fixedSearchFields = []string{
	"access", "name", "node_id", "node_revision", "node_type", "page_ordinal", "page_text",
	"page_text_semantic_info", "projection_version", "retired", "search_generation",
}

// mappedObject is one strict object of an index mapping and its field
// definitions.
type mappedObject struct {
	Dynamic    string                     `json:"dynamic"`
	Properties map[string]json.RawMessage `json:"properties"`
}

// requireFixedSearchMapping reads the mapping of index from OpenSearch and
// requires strict dynamic mapping with exactly the fixed top-level fields and
// the three access fields.
func requireFixedSearchMapping(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	mappingJSON := nativeMapping(t, client, index)
	var mapping, access mappedObject
	if err := json.Unmarshal(mappingJSON, &mapping); err != nil {
		t.Fatalf("decode the mapping of index %s: %v", index, err)
	}
	if err := json.Unmarshal(mapping.Properties["access"], &access); err != nil {
		t.Fatalf("decode the access mapping of index %s: %v", index, err)
	}
	fields, accessFields := slices.Sorted(maps.Keys(mapping.Properties)), slices.Sorted(maps.Keys(access.Properties))
	wantAccess := []string{"generation", "keys", "versions"}
	if mapping.Dynamic != "strict" || access.Dynamic != "strict" || !slices.Equal(fields, fixedSearchFields) || !slices.Equal(accessFields, wantAccess) {
		t.Fatalf("index %s mapping is %q with fields %v and access %q with fields %v, want strict with %v and %v",
			index, mapping.Dynamic, fields, access.Dynamic, accessFields, fixedSearchFields, wantAccess)
	}
}

func savedSparseNodes(t *testing.T, fixture queryFixture, query searchdomain.Query, tokens json.RawMessage) []uuid.UUID {
	t.Helper()
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 65536, BatchSize: 100})
	created, err := fixture.Client.PointInTime.Create(WithTestOperation(t, t.Context()), &opensearchapi.CreatePITReq{
		Indices: []string{query.Index}, Params: &opensearchapi.CreatePITParams{KeepAlive: time.Minute},
	})
	if err != nil || !created.PITID.IsSet() || created.Shards.Failed != 0 {
		t.Fatalf("open saved sparse snapshot: %v, response=%+v", err, created)
	}
	snapshot := searchdomain.Snapshot{PITID: created.PITID.String(), Index: query.Index, QueryTokens: bytes.Clone(tokens)}
	defer func() {
		if err := ranker.Close(WithTestOperation(t, t.Context()), snapshot); err != nil {
			t.Errorf("close saved sparse snapshot: %v", err)
		}
	}()
	// Empty lexical text selects only the saved sparse weights, without inference.
	query.Text = ""
	var nodes []uuid.UUID
	var after json.RawMessage
	for range 10_000 {
		batch, err := ranker.Read(WithTestOperation(t, t.Context()), query, snapshot, after)
		if err != nil {
			t.Fatalf("read saved sparse query: %v", err)
		}
		snapshot.PITID = batch.PITID
		if len(batch.Hits) == 0 {
			slices.SortFunc(nodes, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
			return slices.Compact(nodes)
		}
		for _, hit := range batch.Hits {
			nodes = append(nodes, hit.NodeID)
		}
		after = bytes.Clone(batch.Hits[len(batch.Hits)-1].Sort)
	}
	t.Fatal("saved sparse query did not exhaust")
	return nil
}
