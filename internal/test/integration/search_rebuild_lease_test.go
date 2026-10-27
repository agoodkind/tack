package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

const (
	// rebuildLeaseAttempts is the number of claims of the yellow target the
	// lease test runs.
	rebuildLeaseAttempts = 2
	// faultLease is the worker lease of the fault tests. Each claim pause of
	// a split or switch waits one lease.
	faultLease = 15 * time.Second
)

// faultWorker returns the production worker with the shorter fault lease.
func faultWorker(t *testing.T, fixture queryFixture) *service.SearchWorker {
	t.Helper()
	settings := searchWorkerSettings(runtimePageBytes)
	settings.Lease = faultLease
	return newSearchWorker(t, fixture.Stores, fixture.Adapter, clock.Wall{}, settings)
}

// runBoundedRebuildUntil runs worker slices until done accepts the
// replacement state. Before every slice it requires the index bound of
// [requireIndexBound]. A slice error fails the test.
func runBoundedRebuildUntil(t *testing.T, fixture queryFixture, first string, done func(searchdomain.Rebuild, bool) bool) searchdomain.Rebuild {
	t.Helper()
	return driveRebuild(t, fixture, first, false, func(current searchdomain.Rebuild, found bool, _ []error) bool {
		return done(current, found)
	})
}

// runFaultedRebuildUntil runs worker slices while an injected engine fault
// is active. It records each slice error instead of failing the test, and
// done receives every error recorded so far.
func runFaultedRebuildUntil(t *testing.T, fixture queryFixture, first string, done func(searchdomain.Rebuild, bool, []error) bool) searchdomain.Rebuild {
	t.Helper()
	return driveRebuild(t, fixture, first, true, done)
}

func driveRebuild(t *testing.T, fixture queryFixture, first string, faulted bool, done func(searchdomain.Rebuild, bool, []error) bool) searchdomain.Rebuild {
	t.Helper()
	deadline := time.Now().Add(rebuildDeadline)
	var failures []error
	for time.Now().Before(deadline) {
		current, found, err := fixture.Stores.SearchRebuilds(clock.Wall{}).CurrentRebuild(WithTestOperation(t, t.Context()))
		if err != nil {
			t.Fatalf("read index replacement: %v", err)
		}
		requireIndexBound(t, fixture, first, current, found)
		if done(current, found, failures) {
			return current
		}
		claimed, err := fixture.Worker.RunSlice(WithTestOperation(t, t.Context()))
		if err != nil && !faulted {
			t.Fatalf("run search worker slice: %v", err)
		}
		if err != nil {
			t.Logf("slice failed under the injected fault: %v", err)
			failures = append(failures, err)
		}
		if !claimed {
			time.Sleep(idleSlicePause)
		}
	}
	t.Fatalf("index replacement stayed short of the expected state for %s", rebuildDeadline)
	return searchdomain.Rebuild{}
}

// failedWith reports whether any recorded slice error contains text.
func failedWith(failures []error, text string) bool {
	for _, failure := range failures {
		if strings.Contains(failure.Error(), text) {
			return true
		}
	}
	return false
}

// requireIndexBound requires every node-pages index of the test to be the
// serving index, the replacement target, or the retiring source, which
// allows at most one index of each role. The test creates first before any
// other index. Every index name ends with a UUIDv7, and the names at or after
// first belong to the test.
func requireIndexBound(t *testing.T, fixture queryFixture, first string, current searchdomain.Rebuild, found bool) {
	t.Helper()
	serving, err := fixture.Stores.ServingSearchIndex(WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatalf("read serving index: %v", err)
	}
	roles := map[string]string{serving: "serving"}
	if found && current.State == searchdomain.RebuildRetiring {
		roles[current.SourceIndex] = "retiring"
	}
	if found && current.State != searchdomain.RebuildRetiring {
		roles[current.TargetIndex] = "replacement"
	}
	response, err := fixture.Client.Cat.Indices(WithTestOperation(t, t.Context()), &opensearchapi.CatIndicesReq{Indices: []string{"node-pages-*"}})
	if err != nil {
		t.Fatalf("list search indexes: %v", err)
	}
	for _, index := range response.Indices {
		if _, allowed := roles[index.Index]; index.Index >= first && !allowed {
			t.Fatalf("index %s exists in replacement state %q outside the roles %v", index.Index, current.State, roles)
		}
	}
}

// setShardLimit writes the persistent cluster limit of open shards per data
// node. The value null restores the default. Any other value also registers
// a cleanup that restores the default.
func setShardLimit(t *testing.T, fixture queryFixture, value string) {
	t.Helper()
	if value != "null" {
		t.Cleanup(func() {
			if err := setClusterSetting(context.WithoutCancel(t.Context()), fixture, "cluster.max_shards_per_node", "null"); err != nil {
				t.Errorf("restore the shard limit: %v", err)
			}
		})
	}
	if err := setClusterSetting(WithTestOperation(t, t.Context()), fixture, "cluster.max_shards_per_node", value); err != nil {
		t.Fatalf("set the shard limit to %s: %v", value, err)
	}
}

// requireGreen requires the engine to report green health for index.
func requireGreen(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	response, err := client.Cluster.Health(WithTestOperation(t, t.Context()), &opensearchapi.ClusterHealthReq{Indices: []string{index}})
	if err != nil || response.Status != "green" {
		t.Fatalf("health of %s = %+v, error = %v, want green", index, response, err)
	}
}

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
	rebuild, err := rebuilds.BeginRebuild(WithTestOperation(t, t.Context()), searchdomain.BeginRebuild{
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
		claimed, err := worker.RunSlice(WithTestOperation(t, t.Context()))
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
		current, found, err := rebuilds.CurrentRebuild(WithTestOperation(t, t.Context()))
		if err != nil || !found || current.State != searchdomain.RebuildCreating || current.Failure != "" {
			t.Fatalf("replacement after step %d = %+v, found %t, error %v, want creating without failure", attempt, current, found, err)
		}
		attempt++
	}
}
