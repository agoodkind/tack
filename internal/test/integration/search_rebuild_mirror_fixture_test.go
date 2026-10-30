package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// mirrorRun stores the environment, the delay proxy, the replacement index, the node,
// and the two abandonment times from one runTwoMirrorMisses call.
type mirrorRun struct {
	env                             *migrationEnv
	proxy                           *pageDelayProxy
	target                          string
	nodeID                          uuid.UUID
	firstAbandoned, secondAbandoned time.Time
}

// runTwoMirrorMisses creates a delayed environment with a 60-second write horizon and a
// live-first worker.
// runTwoMirrorMisses configures the public alias to select the serving index and begins a full
// replacement.
// runTwoMirrorMisses runs worker slices until the replacement enters the switching state with
// claims enabled, then stores a node with several pages.
//
// The proxy delays the replacement index write for page 1 until the worker abandons the write.
// steps.duringFirstHold runs during that delay when it is non-nil.
// runTwoMirrorMisses fails the test when steps.duringFirstHold returns an error.
// steps.afterFirstMiss runs after that abandonment when it is non-nil.
// The proxy then delays the replacement index write for page 3 until the worker abandons it.
//
// runTwoMirrorMisses requires the second abandonment within mirrorFreshnessBound after the first.
// runTwoMirrorMisses requires the serving index to store every page of the node within
// mirrorFreshnessBound after the second abandonment.
func runTwoMirrorMisses(t *testing.T, steps mirrorSteps) mirrorRun {
	t.Helper()
	env, proxy, target := startMirrorRebuild(t)
	nodeID := uuid.Must(uuid.NewV7())
	proxy.targetIn(nodeID, mirrorFirstOrdinal, target)
	held := steps.startDuringHold(t, env, proxy, nodeID)
	env.putNode(t, nodeID, strings.Repeat(timingText, 40))
	first, abandoned := awaitAbandonedWrite(t, env, proxy, time.Now().Add(mirrorSwitchDeadline))
	if !abandoned {
		t.Fatalf("the worker did not abandon the delayed write of page %d to %s", mirrorFirstOrdinal, target)
	}
	if err := <-held; err != nil {
		t.Fatalf("change node %s during the delayed write: %v", nodeID, err)
	}
	if steps.afterFirstMiss != nil {
		steps.afterFirstMiss(t, env, nodeID)
	}
	proxy.targetIn(nodeID, mirrorSecondOrdinal, target)
	bound := first.Add(mirrorFreshnessBound)
	second, abandoned := awaitAbandonedWrite(t, env, proxy, bound)
	if !abandoned {
		t.Fatalf("serving index %s lacked pages of node %s at %s, %s after the mirror write of page %d was abandoned: %s",
			env.Index, nodeID, bound, mirrorFreshnessBound, mirrorFirstOrdinal, indexMismatch(t, env, nodeID))
	}
	bound = second.Add(mirrorFreshnessBound)
	if !runSlicesUntil(t, env, bound, func() bool { return indexMismatch(t, env, nodeID) == "" }) {
		t.Fatalf("serving index %s lacked pages of node %s at %s, %s after the mirror write was abandoned: %s",
			env.Index, nodeID, bound, mirrorFreshnessBound, indexMismatch(t, env, nodeID))
	}
	return mirrorRun{env: env, proxy: proxy, target: target, nodeID: nodeID, firstAbandoned: first, secondAbandoned: second}
}

// startMirrorRebuild creates a delayed environment with a 60-second write horizon and a live-first
// worker. The proxy never sends an abandoned write.
// The helper configures the public alias to select the serving index.
// The helper runs worker slices until the worker cannot claim any work and begins a full replacement.
// The helper runs worker slices until the replacement enters the switching state with claims enabled.
// The helper returns the environment, the proxy, and the replacement index.
func startMirrorRebuild(t *testing.T) (*migrationEnv, *pageDelayProxy, string) {
	t.Helper()
	env, proxy := newDelayedMigrationEnv(t, timingPageBytes, 0)
	env.WriteHorizon = mirrorWriteHorizon
	env.Worker = newLiveFirstWorker(t, env)
	proxy.forwardAbandonedAfter(0)
	if err := env.Adapter.SetAlias(integration.WithTestOperation(t, t.Context()), search.PublicAlias, env.Index); err != nil {
		t.Fatalf("set public alias: %v", err)
	}
	env.runIdle(t)
	target := beginMirrorRebuild(t, env).TargetIndex
	runRebuildSlicesUntil(t, env, func(current searchdomain.Rebuild) bool {
		return current.State == searchdomain.RebuildSwitching && !current.Paused
	})
	return env, proxy, target
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
