package integration

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// rebuildLeaseAttempts is the number of claims of the yellow target the
// lease test runs.
const rebuildLeaseAttempts = 2

// TestSearchRebuildStepFitsLease requires each replacement step to end
// before its claim lease ends while the target index is not green. The
// single-node engine cannot assign the one replica the target requests, and
// the target health stays yellow. Each claim must end without recording a
// failure, and the replacement must stay in the creating state for a later
// retry.
func TestSearchRebuildStepFitsLease(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, _ := newSearchIndex(t, stores)
	settings := searchWorkerSettings(runtimePageBytes)
	worker := newSearchWorker(t, stores, adapter, clock.Wall{}, settings)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	rebuild, err := rebuilds.BeginRebuild(t.Context(), searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 24, Replicas: 1, Restored: false, Reason: "test",
	})
	if err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	t.Cleanup(func() { deleteNativeIndex(t, client, rebuild.TargetIndex) })
	deadline := time.Now().Add(rebuildDeadline)
	for attempt := 0; attempt < rebuildLeaseAttempts; {
		if time.Now().After(deadline) {
			t.Fatalf("the replacement was claimed %d times in %s, want %d", attempt, rebuildDeadline, rebuildLeaseAttempts)
		}
		started := time.Now()
		claimed, err := worker.RunSlice(t.Context())
		elapsed := time.Since(started)
		if err != nil {
			t.Fatalf("replacement step %d: %v", attempt, err)
		}
		if !claimed {
			time.Sleep(settings.IdleInterval)
			continue
		}
		if elapsed >= settings.Lease {
			t.Fatalf("replacement step %d ran %s, want less than the %s lease", attempt, elapsed, settings.Lease)
		}
		current, found, err := rebuilds.CurrentRebuild(t.Context())
		if err != nil || !found || current.State != searchdomain.RebuildCreating || current.Failure != "" {
			t.Fatalf("replacement after step %d = %+v, found %t, error %v, want creating without failure", attempt, current, found, err)
		}
		attempt++
	}
}
