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

const mirrorResumeFreshnessBound = mirrorWriteHorizon - mirrorOperationTimeout

// TestSearchRebuildMirrorMissSurvivesResumedSwitch uses two valid live claims.
// The first cancellation records a live pending deadline during pause. The second
// timed-out mirror checkpoints copy work during that pause. The rebuild resumes
// claims, and the first node must finish serving before its replacement deadline.
func TestSearchRebuildMirrorMissSurvivesResumedSwitch(t *testing.T) {
	env, proxy, target := startMirrorRebuild(t)
	firstID, secondID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	proxy.targetIn(firstID, mirrorExpiredOrdinal, target)
	firstContext, cancelFirst := context.WithCancel(integration.WithTestOperation(t, t.Context()))
	defer cancelFirst()
	env.putNode(t, firstID, strings.Repeat(timingText, 40))
	firstDone := startMirrorSlice(firstContext, env.Worker)
	waitMirrorHold(t, proxy)
	proxy.targetIn(secondID, mirrorExpiredOrdinal, target)
	env.putNode(t, secondID, strings.Repeat(timingText, 40))
	secondDone := startMirrorSlice(integration.WithTestOperation(t, t.Context()), newLiveFirstWorker(t, env))
	waitMirrorHold(t, proxy)
	pauser := newLiveFirstWorker(t, env)
	pauseReplacement(t, env, pauser)
	cancelFirst()
	if result := <-firstDone; !result.claimed {
		t.Fatalf("first live slice did not claim work: %v", result.err)
	}
	firstPending := singleNodePending(t, env, firstID).record
	if firstPending.Index != target || !firstPending.Deadline.Equal(firstPending.AbandonedAt.Add(mirrorWriteHorizon)) {
		t.Fatalf("first pending mirror write = %+v, want the original replacement deadline", firstPending)
	}
	if result := <-secondDone; !result.claimed || result.err != nil {
		t.Fatalf("second valid live slice did not checkpoint its mirror miss: claimed %t, error %v", result.claimed, result.err)
	}
	rebuilds := env.Stores.SearchRebuilds(clock.Wall{})
	if pending, err := rebuilds.CopiesPending(integration.WithTestOperation(t, t.Context())); err != nil || !pending {
		t.Fatalf("copies pending = %t, error %v, want the second claim's copy", pending, err)
	}
	current, found, err := rebuilds.CurrentRebuild(integration.WithTestOperation(t, t.Context()))
	if err != nil || !found || !current.Paused || current.State != searchdomain.RebuildSwitching {
		t.Fatalf("replacement after the second checkpoint = %+v, found %t, error %v, want paused Switching", current, found, err)
	}
	runRebuildSlicesUntil(t, env, func(rebuild searchdomain.Rebuild) bool {
		return rebuild.State == searchdomain.RebuildSwitching && !rebuild.Paused
	})
	freshBy := firstPending.AbandonedAt.Add(mirrorResumeFreshnessBound)
	if !runSlicesUntil(t, env, freshBy, func() bool { return indexMismatch(t, env, firstID) == "" }) {
		t.Fatalf("serving index lacks node %s before its mirror deadline: %s", firstID, indexMismatch(t, env, firstID))
	}
	requireSwitchAfterCopies(t, env, target)
	run := mirrorRun{env: env, proxy: proxy, target: target, nodeID: firstID, firstAbandoned: firstPending.AbandonedAt, secondAbandoned: time.Time{}}
	requireReplacementPages(t, run)
	requireResendAfterHorizon(t, proxy, firstID, mirrorExpiredOrdinal, target, firstPending.AbandonedAt)
	requirePermanentMirrorAudit(t, env, target)
}

// startMirrorSlice runs the production worker and preserves a panic as a test error.
func startMirrorSlice(ctx context.Context, worker *service.SearchWorker) <-chan sliceResult {
	done := make(chan sliceResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				done <- sliceResult{claimed: false, err: fmt.Errorf("mirror slice panicked: %v", recovered)}
			}
		}()
		claimed, err := worker.RunSlice(ctx)
		done <- sliceResult{claimed: claimed, err: err}
	}()
	return done
}

func waitMirrorHold(t *testing.T, proxy *pageDelayProxy) {
	t.Helper()
	proxy.mutex.Lock()
	started := proxy.started
	proxy.mutex.Unlock()
	select {
	case <-started:
	case <-time.After(mirrorOperationTimeout):
		t.Fatalf("the proxy did not start the delayed mirror write within %s", mirrorOperationTimeout)
	}
}
