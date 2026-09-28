package integration

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rebuildTestPauseLimit shortens the replacement pause limit for the pause
// test. It exceeds the 30 second worker lease, as validation requires.
const rebuildTestPauseLimit = 45 * time.Second

// TestSearchRebuildFailsAfterPauseLimit requires a split to fail once the
// pause limit passes while the split target is not green. The single-node
// engine cannot assign the one replica the split target requests. The
// replacement must enter the failed state, and the failure path must delete
// the target, clear the source write block, and keep the source as the
// serving index.
func TestSearchRebuildFailsAfterPauseLimit(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, source := newSearchIndex(t, stores)
	settings := searchWorkerSettings(runtimePageBytes)
	settings.ReplacementPauseLimit = rebuildTestPauseLimit
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, settings)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	rebuild, err := rebuilds.BeginRebuild(t.Context(), searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementSplit, PrimaryShards: 2, RoutingShards: 8, Replicas: 1, Restored: false, Reason: "test",
	})
	if err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	t.Cleanup(func() { deleteNativeIndex(t, client, rebuild.TargetIndex) })
	failed := false
	deadline := time.Now().Add(rebuildDeadline)
	for {
		current, found, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		if !found {
			break
		}
		failed = failed || current.State == searchdomain.RebuildFailed
		if time.Now().After(deadline) {
			t.Fatalf("index replacement stayed in %s for %s", current.State, rebuildDeadline)
		}
		claimed, err := worker.RunSlice(t.Context())
		if err != nil {
			t.Fatalf("replacement step in %s: %v", current.State, err)
		}
		if !claimed {
			time.Sleep(settings.IdleInterval)
		}
	}
	if !failed {
		t.Fatal("the replacement finished without entering the failed state")
	}
	if _, err := adapter.IndexSettings(t.Context(), rebuild.TargetIndex); err == nil {
		t.Fatalf("failed target %s still exists", rebuild.TargetIndex)
	}
	requireWritable(t, client, source)
	serving, err := stores.ServingSearchIndex(t.Context())
	if err != nil || serving != source {
		t.Fatalf("serving index = %q, error = %v, want %q", serving, err, source)
	}
}

// requireWritable requires index to have no write block.
func requireWritable(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	response, err := client.Indices.Settings.Get(t.Context(), &opensearchapi.SettingsGetReq{Indices: []string{index}})
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
	if decoded.Index.Blocks.Write == "true" {
		t.Fatalf("index %s still blocks writes", index)
	}
}
