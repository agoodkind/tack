package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// targetIn selects a page write by nodeID, ordinal, and physical index
// and clears the previous delay state. pageDelayProxy delays the next matching write.
func (proxy *pageDelayProxy) targetIn(nodeID uuid.UUID, ordinal uint64, index string) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.nodeID, proxy.ordinal, proxy.index = nodeID.String(), ordinal, index
	proxy.delayed, proxy.started = false, make(chan struct{})
	proxy.heldFrom, proxy.heldUntil, proxy.abandoned = time.Time{}, time.Time{}, false
}

// awaitAbandonedWrite runs worker slices one at a time until the proxy reports abandonment
// or deadline passes. The helper checks the proxy after every slice.
// It waits idleClaimPause after a slice fails or does not claim work.
// It returns the time the proxy observed the abandonment and true when the proxy reports it.
// It returns the zero time and false when deadline passes.
func awaitAbandonedWrite(t *testing.T, env *migrationEnv, proxy *pageDelayProxy, deadline time.Time) (time.Time, bool) {
	t.Helper()
	for time.Now().Before(deadline) {
		claimed, err := env.Worker.RunSlice(integration.WithTestOperation(t, t.Context()))
		if err != nil {
			t.Logf("run a slice: %v", err)
		}
		if _, until, abandoned := proxy.held(); abandoned {
			return until, true
		}
		if !claimed || err != nil {
			time.Sleep(idleClaimPause)
		}
	}
	return time.Time{}, false
}

// beginMirrorRebuild begins one full replacement through the production store with
// one primary shard and eight routing shards. The replacement does not use replicas.
// beginMirrorRebuild deletes the replacement index when the test ends.
func beginMirrorRebuild(t *testing.T, env *migrationEnv) searchdomain.Rebuild {
	t.Helper()
	request := searchdomain.BeginRebuild{Mode: searchdomain.ReplacementFull, PrimaryShards: 1, RoutingShards: 8, Replicas: 0, Restored: false, Reason: "test"}
	rebuild, err := env.Stores.SearchRebuilds(clock.Wall{}).BeginRebuild(integration.WithTestOperation(t, t.Context()), request)
	if err != nil {
		t.Fatalf("begin index replacement: %v", err)
	}
	t.Cleanup(func() {
		deleteRequest := &opensearchapi.IndicesDeleteReq{Indices: []string{rebuild.TargetIndex}}
		if _, err := env.Client.Indices.Delete(context.WithoutCancel(integration.WithTestOperation(t, t.Context())), deleteRequest); err != nil {
			t.Errorf("delete replacement index %s: %v", rebuild.TargetIndex, err)
		}
	})
	return rebuild
}

// runRebuildSlicesUntil reads the replacement before each worker slice and returns
// the record when done accepts it.
// runRebuildSlicesUntil fails the test when the replacement record is missing or
// mirrorSwitchDeadline expires.
// runRebuildSlicesUntil reads the state after every slice.
// The test observes the retiring state before another slice runs the retire step.
func runRebuildSlicesUntil(t *testing.T, env *migrationEnv, done func(searchdomain.Rebuild) bool) searchdomain.Rebuild {
	t.Helper()
	rebuilds := env.Stores.SearchRebuilds(clock.Wall{})
	deadline := time.Now().Add(mirrorSwitchDeadline)
	for time.Now().Before(deadline) {
		current, found, err := rebuilds.CurrentRebuild(integration.WithTestOperation(t, t.Context()))
		if err != nil || !found {
			t.Fatalf("read index replacement: found %t, error %v", found, err)
		}
		if done(current) {
			return current
		}
		claimed, err := env.Worker.RunSlice(integration.WithTestOperation(t, t.Context()))
		if err != nil {
			t.Logf("run a slice: %v", err)
		}
		if !claimed || err != nil {
			time.Sleep(idleClaimPause)
		}
	}
	t.Fatalf("index replacement stayed short of the expected state for %s", mirrorSwitchDeadline)
	return searchdomain.Rebuild{}
}

// newLiveFirstWorker creates the production worker with the settings of
// migrationEnv.newWorker except the live class weight.
// newLiveFirstWorker sets the live class weight to mirrorLiveWeight.
func newLiveFirstWorker(t *testing.T, env *migrationEnv) *service.SearchWorker {
	t.Helper()
	policies := env.Stores.SearchPolicySet()
	rollouts := env.Stores.SearchRollouts(clock.Wall{}, policies)
	settings := config.SearchWorkerSettings{
		PageBytes: env.PageBytes, MaxPages: 32, MaxBytes: 5 << 20, SliceBudget: 2 * time.Second,
		OperationTimeout: 10 * time.Second, Lease: 30 * time.Second, IdleInterval: 50 * time.Millisecond,
		Concurrency: 1, ClassWeights: map[string]int{"live": mirrorLiveWeight, "access": 1, "cleanup": 1, "rescan": 1, "rollout": 1, "rebuild": 1, "copy": 1},
		ReplacementPauseLimit: config.SearchReplacementPauseLimit, WriteCompletionHorizon: env.WriteHorizon,
	}
	worker, err := service.NewSearchWorker(service.SearchWorkerPorts{
		Store: env.Stores.SearchWork(clock.Wall{}), Reader: env.Stores.SearchContent(policies),
		Access: env.Stores.SearchAccess(policies), Writer: env.Adapter, Rollouts: rollouts, Sessions: rollouts,
		Documents: env.Adapter, Rebuilds: env.Stores.SearchRebuilds(clock.Wall{}), Replacer: env.Adapter,
		Audit: appruntime.SearchWorkerAudit(env.Stores),
	}, clock.Wall{}, settings, 0)
	if err != nil {
		t.Fatalf("create search worker: %v", err)
	}
	return worker
}
