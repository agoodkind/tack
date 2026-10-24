package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// rebuildTestPauseLimit shortens the replacement pause limit for the
	// pause test. It exceeds the 30 second worker lease, as validation
	// requires. The first split attempt ends about 50 seconds after the
	// pause, after one lease and a green wait of 20 seconds. That attempt
	// ends before the limit and leaves the source write-blocked. The second
	// attempt ends after the limit and fails the replacement.
	rebuildTestPauseLimit = 75 * time.Second
	// readOnlySetting blocks document writes and metadata writes. The engine
	// then refuses alias changes and every other settings change on the
	// index.
	readOnlySetting = "index.blocks.read_only"
	// writeBlockSetting blocks document writes and keeps reads.
	writeBlockSetting = "index.blocks.write"
)

// TestSearchRebuildFailsAfterPauseLimit requires a split to fail once the
// pause limit passes while the split target is not green. The single-node
// engine cannot assign the one replica the split target requests. While the
// split keeps the source write-blocked, the public alias must select the
// source, public search must read it, and FoundationDB mutations must commit
// and stay queued. The replacement must then enter the failed state, and the
// failure path must delete the target, clear the source write block, and keep
// the source as the serving index. After recovery, the worker must index the
// queued mutations in the source.
func TestSearchRebuildFailsAfterPauseLimit(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	settings := searchWorkerSettings(runtimePageBytes)
	settings.ReplacementPauseLimit = rebuildTestPauseLimit
	fixture.Worker = newSearchWorker(t, fixture.Stores, fixture.Adapter, clock.Wall{}, settings)
	workspace := fixture.Workspaces[0]
	entry := entryPoint(t, fixture, workspace)
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	indexed := putOpaqueNode(t, fixture, kind, entry, "indexed blocked node", "ochre blocked indexed", readerExcludedValue)
	drainSearchWork(t, fixture.Worker, 2000)
	source := fixture.Index
	rebuild := beginRebuild(t, fixture, searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementSplit, PrimaryShards: 2, RoutingShards: 24, Replicas: 1, Restored: false, Reason: "test",
	})
	runBoundedRebuildUntil(t, fixture, source, func(current searchdomain.Rebuild, found bool) bool {
		return found && current.State == searchdomain.RebuildCreating && current.Paused && writeBlocked(t, fixture.Client, source)
	})

	requireServing(t, fixture, source, "")
	if results := callEverySearchPage(t, "ochre blocked", fixture.Harness); !slices.Contains(results.IDs, indexed) {
		t.Fatalf("search during the split write block returned %v, want node %s from the source", results.IDs, indexed)
	}
	queued := putOpaqueNode(t, fixture, kind, entry, "queued blocked node", "ochre blocked queued", readerExcludedValue)
	editOpaqueNode(t, fixture, kind, indexed, "ochre blocked revised")
	failed := runBoundedRebuildUntil(t, fixture, source, func(current searchdomain.Rebuild, found bool) bool {
		return !found || current.State == searchdomain.RebuildFailed
	})
	requireFailure(t, failed, "wait for green target "+rebuild.TargetIndex)
	requireQueued(t, fixture, source, queued, indexed)
	requireRecovered(t, fixture, source, rebuild.TargetIndex)
	drainSearchWork(t, fixture.Worker, 2000)
	requireCurrentPages(t, fixture, source, []uuid.UUID{indexed, queued})
}

// setIndexBlock sets the boolean block setting of index, such as
// readOnlySetting or writeBlockSetting. When blocked is true, it also
// registers a cleanup that clears the block before the index cleanup deletes
// the index.
func setIndexBlock(t *testing.T, client *opensearchapi.Client, index, setting string, blocked bool) {
	t.Helper()
	if blocked {
		t.Cleanup(func() {
			body := strings.NewReader(fmt.Sprintf(`{%q:false}`, setting))
			_, _ = client.Indices.Settings.Put(context.WithoutCancel(t.Context()), opensearchapi.SettingsPutReq{Indices: []string{index}, Body: body})
		})
	}
	body := strings.NewReader(fmt.Sprintf(`{%q:%t}`, setting, blocked))
	response, err := client.Indices.Settings.Put(WithTestOperation(t, t.Context()), opensearchapi.SettingsPutReq{Indices: []string{index}, Body: body})
	if err != nil || !response.Acknowledged {
		t.Fatalf("set %s %t on %s: %v", setting, blocked, index, err)
	}
}

// clearIndexBlock clears the boolean block setting of index.
func clearIndexBlock(t *testing.T, client *opensearchapi.Client, index, setting string) {
	t.Helper()
	setIndexBlock(t, client, index, setting, false)
}

// writeBlocked reports whether index has the write block.
func writeBlocked(t *testing.T, client *opensearchapi.Client, index string) bool {
	t.Helper()
	response, err := client.Indices.Settings.Get(WithTestOperation(t, t.Context()), &opensearchapi.SettingsGetReq{Indices: []string{index}})
	if err != nil {
		t.Fatalf("read settings of %s: %v", index, err)
	}
	entry, exists := response.GetIndices()[index]
	if !exists {
		t.Fatalf("settings of %s are absent", index)
	}
	var decoded struct {
		Index struct {
			Blocks struct {
				Write string `json:"write"`
			} `json:"blocks"`
		} `json:"index"`
	}
	if err := json.Unmarshal(entry.Settings, &decoded); err != nil {
		t.Fatalf("decode settings of %s: %v", index, err)
	}
	return decoded.Index.Blocks.Write == "true"
}

// requireWritable requires index to have no write block.
func requireWritable(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	if writeBlocked(t, client, index) {
		t.Fatalf("index %s still blocks writes", index)
	}
}

// requireFailure requires the failed replacement to record text.
func requireFailure(t *testing.T, failed searchdomain.Rebuild, text string) {
	t.Helper()
	if failed.State != searchdomain.RebuildFailed || !strings.Contains(failed.Failure, text) {
		t.Fatalf("replacement in %s failed with %q, want a failure that contains %q", failed.State, failed.Failure, text)
	}
}

// requireRecovered runs the failed replacement to its end in a new worker
// that reads only the durable failed state. The failed state resumes claims
// before recovery clears the source write block. A live write that the
// worker claims first fails on that block and stays pending. Every other
// slice error fails the test. The source must accept writes and stay the
// serving index, and the failed target must be gone.
func requireRecovered(t *testing.T, fixture queryFixture, source, target string) {
	t.Helper()
	fixture.Worker = faultWorker(t, fixture)
	var failures []error
	runFaultedRebuildUntil(t, fixture, source, func(_ searchdomain.Rebuild, found bool, recorded []error) bool {
		failures = recorded
		return !found
	})
	for _, failure := range failures {
		if !strings.Contains(failure.Error(), "to "+source+":") || !strings.Contains(failure.Error(), "index write") {
			t.Fatalf("recovery slice failed outside the source write block: %v", failure)
		}
	}
	requireWritable(t, fixture.Client, source)
	requireServing(t, fixture, source, "")
	if _, err := fixture.Adapter.IndexSettings(WithTestOperation(t, t.Context()), target); err == nil {
		t.Fatalf("failed target %s still exists", target)
	}
}

// requireQueued requires each node to exist in FoundationDB while index
// lacks its current pages.
func requireQueued(t *testing.T, fixture queryFixture, index string, nodes ...uuid.UUID) {
	t.Helper()
	for _, nodeID := range nodes {
		if view, err := fixture.Stores.Views.Get(WithTestOperation(t, t.Context()), nodeID); err != nil || view == nil {
			t.Fatalf("read committed node %s: %v", nodeID, err)
		}
		if pagesCurrent(t, fixture, index, nodeID) {
			t.Fatalf("index %s already has the current pages of node %s", index, nodeID)
		}
	}
}
