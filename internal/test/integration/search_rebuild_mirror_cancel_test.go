package integration_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
)

// mirrorOperationTimeout equals the test worker's OperationTimeout.
// The canceled delayed write ends before mirrorOperationTimeout elapses.
const mirrorOperationTimeout = 10 * time.Second

// sliceResult stores the claimed flag and error that RunSlice returns in another
// goroutine.
type sliceResult struct {
	claimed bool
	err     error
}

// TestSearchRebuildMirrorMissSurvivesCanceledSlice calls startMirrorRebuild and stores
// a node with several pages. The proxy delays the replacement index write of page 0.
//
// The test runs one worker slice with a cancelable context. A goroutine from
// startDuringHold cancels that context after the proxy starts delaying the write.
// A worker stop cancels the slice context in the same way.
//
// The canceled context ends the delayed mirror write. CompletePage fails with the
// canceled context. The node content and page 0 document remain unchanged.
//
// The test calls requireSwitchAfterCopies and requireReplacementPages. The test
// requires a second replacement index write of the page 0 document at or after the
// abandonment time plus mirrorWriteHorizon minus one second of clock skew.
//
// A worker that sets the missed write deadline to now after cancellation does not
// store the pending write record. Its next live claim writes the page 0 document
// to the replacement index before the horizon ends.
func TestSearchRebuildMirrorMissSurvivesCanceledSlice(t *testing.T) {
	env, proxy, target := startMirrorRebuild(t)
	nodeID := uuid.Must(uuid.NewV7())
	proxy.targetIn(nodeID, mirrorExpiredOrdinal, target)
	ctx, cancel := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	defer cancel()
	cancelDuringHold := func(*testing.T, *migrationEnv, uuid.UUID) func(context.Context) error {
		return func(context.Context) error {
			cancel()
			return nil
		}
	}
	held := mirrorSteps{duringFirstHold: cancelDuringHold, afterFirstMiss: nil}.startDuringHold(t, env, proxy, nodeID)
	env.putNode(t, nodeID, strings.Repeat(timingText, 40))
	claimed, err := env.Worker.RunSlice(ctx)
	if holdErr := <-held; holdErr != nil {
		t.Fatalf("cancel the slice during the delayed write: %v", holdErr)
	}
	_, abandonedAt, abandoned := proxy.held()
	if !claimed || !abandoned {
		t.Fatalf("the canceled slice claimed %t and abandoned the write of page %d %t (error %v), want both",
			claimed, mirrorExpiredOrdinal, abandoned, err)
	}
	run := mirrorRun{env: env, proxy: proxy, target: target, nodeID: nodeID, firstAbandoned: abandonedAt, secondAbandoned: time.Time{}}
	requireSwitchAfterCopies(t, env, target)
	requireReplacementPages(t, run)
	requireResendAfterHorizon(t, proxy, nodeID, mirrorExpiredOrdinal, target, abandonedAt)
	requirePermanentMirrorAudit(t, env, target)
}

// TestSearchRebuildMirrorMissHeldAcrossPausedSwitch calls startMirrorRebuild, creates
// a second live-first worker named pauser, and stores a node with several pages.
// The proxy delays the replacement index write of page 0.
//
// The first worker runs the live slice in another goroutine with a cancelable
// context. While the proxy delays the write, pauseReplacement runs pauser slices
// until the replacement pauses claims in the switching state.
//
// The test then cancels the live slice. CompletePage fails, and RecordMirrorMiss
// runs while claims are paused. The test requires that cancellation ends the delayed
// write before mirrorOperationTimeout elapses. The test requires that a copy item
// is not pending.
//
// The test runs worker slices until the replacement enters the retiring state.
// The test then runs worker slices until the new serving index stores every page
// of the node. That wait uses a deadline of the abandonment time plus
// mirrorWriteHorizon plus mirrorFreshnessBound.
//
// The test requires the second replacement index write of the page 0 document at
// or after the abandonment time plus mirrorWriteHorizon minus one second of clock
// skew.
func TestSearchRebuildMirrorMissHeldAcrossPausedSwitch(t *testing.T) {
	env, proxy, target := startMirrorRebuild(t)
	pauser := newLiveFirstWorker(t, env)
	nodeID := uuid.Must(uuid.NewV7())
	proxy.targetIn(nodeID, mirrorExpiredOrdinal, target)
	proxy.mutex.Lock()
	started := proxy.started
	proxy.mutex.Unlock()
	env.putNode(t, nodeID, strings.Repeat(timingText, 40))
	ctx, cancel := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	defer cancel()
	done := make(chan sliceResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- sliceResult{claimed: false, err: fmt.Errorf("the live slice panicked: %v", recovered)}
			}
		}()
		claimed, err := env.Worker.RunSlice(ctx)
		done <- sliceResult{claimed: claimed, err: err}
	}()
	select {
	case <-started:
	case <-time.After(mirrorSwitchDeadline):
		t.Fatalf("the proxy did not delay the write of page %d within %s", mirrorExpiredOrdinal, mirrorSwitchDeadline)
	}
	pauseReplacement(t, env, pauser)
	cancel()
	result := <-done
	heldFrom, abandonedAt, abandoned := proxy.held()
	if !result.claimed || !abandoned || abandonedAt.Sub(heldFrom) >= mirrorOperationTimeout {
		t.Fatalf("the live slice claimed %t and abandoned the write %t after %s (error %v), want a cancellation before %s",
			result.claimed, abandoned, abandonedAt.Sub(heldFrom), result.err, mirrorOperationTimeout)
	}
	if pending, err := env.Stores.SearchRebuilds(clock.Wall{}).CopiesPending(integration.WithTestOperation(t, t.Context())); err != nil || pending {
		t.Fatalf("copies pending = %t, error = %v, want no copy item added while claims are paused", pending, err)
	}
	runRebuildSlicesUntil(t, env, func(current searchdomain.Rebuild) bool { return current.State == searchdomain.RebuildRetiring })
	replacement := *env
	replacement.Index = target
	bound := abandonedAt.Add(mirrorWriteHorizon + mirrorFreshnessBound)
	if !runSlicesUntil(t, env, bound, func() bool { return indexMismatch(t, &replacement, nodeID) == "" }) {
		t.Fatalf("the new serving index %s lacked pages of node %s at %s: %s", target, nodeID, bound, indexMismatch(t, &replacement, nodeID))
	}
	requireResendAfterHorizon(t, proxy, nodeID, mirrorExpiredOrdinal, target, abandonedAt)
}

// pauseReplacement runs pauser slices until the replacement is in the switching
// state with claims paused. pauseReplacement fails the test when that state does
// not occur within mirrorOperationTimeout or the replacement record is missing.
func pauseReplacement(t *testing.T, env *migrationEnv, pauser *service.SearchWorker) {
	t.Helper()
	rebuilds := env.Stores.SearchRebuilds(clock.Wall{})
	deadline := time.Now().Add(mirrorOperationTimeout)
	for time.Now().Before(deadline) {
		if _, err := pauser.RunSlice(integration.WithTestOperation(t, t.Context())); err != nil {
			t.Logf("run a pausing slice: %v", err)
		}
		current, found, err := rebuilds.CurrentRebuild(integration.WithTestOperation(t, t.Context()))
		if err != nil || !found {
			t.Fatalf("read index replacement: found %t, error %v", found, err)
		}
		if current.State == searchdomain.RebuildSwitching && current.Paused {
			return
		}
	}
	t.Fatalf("the replacement did not pause claims within %s", mirrorOperationTimeout)
}
