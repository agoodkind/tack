package integration_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// mirrorWriteHorizon sets the test worker's WriteCompletionHorizon to 60 seconds.
	// The proxy never sends the abandoned mirror write to the engine.
	mirrorWriteHorizon = 60 * time.Second
	// mirrorFreshnessBound limits the time between the first and second abandonments to 30 seconds.
	// The test also requires the serving index to store every node page within this duration after
	// the second abandonment. This duration is shorter than mirrorWriteHorizon.
	mirrorFreshnessBound = 30 * time.Second
	// mirrorSwitchDeadline limits the wait for the first abandonment and each wait for a requested
	// replacement state to ten minutes. Each claim pause lasts one worker lease.
	mirrorSwitchDeadline = 10 * time.Minute
	// mirrorLiveWeight sets the test worker's live class weight to 1000.
	// The worker claims live work before replacement work whenever live work is claimable.
	mirrorLiveWeight = 1000
	// mirrorFirstOrdinal sets the page ordinal to 1 for the first delayed replacement index write.
	mirrorFirstOrdinal = 1
	// mirrorSecondOrdinal sets the page ordinal to 3 for the second delayed replacement index write.
	mirrorSecondOrdinal = 3
)

// TestSearchRebuildMirrorDelayKeepsServingFresh verifies serving progress during delayed
// replacement writes. The test starts a full replacement through the production store.
// It runs worker slices until the replacement enters the switching state with claims not paused.
//
// The test stores a node with several pages. The proxy delays the replacement write for page 1
// until the worker abandons it at the ten-second OperationTimeout. The test then selects page 3,
// and the proxy delays that replacement write until the worker abandons it.
// The proxy never sends either abandoned write to OpenSearch.
//
// The test requires the second abandonment within 30 seconds after the first abandonment.
// The copy item for page 1 remains pending at the second abandonment.
// The test requires the serving index to store every node page within 30 seconds after
// the second abandonment.
//
// The test calls requireSwitchAfterCopies and requires the replacement index to store every
// node page at the current revision. For page 1 and page 3, the test requires a second replacement
// write at or after that page's abandonment time plus mirrorWriteHorizon minus one second.
// The timing check allows one second of clock skew.
func TestSearchRebuildMirrorDelayKeepsServingFresh(t *testing.T) {
	env, proxy := newDelayedMigrationEnv(t, timingPageBytes, 0)
	env.WriteHorizon = mirrorWriteHorizon
	env.Worker = newLiveFirstWorker(t, env)
	proxy.forwardAbandonedAfter(0)
	ctx := integration.WithTestOperation(t, t.Context())
	if err := env.Adapter.SetAlias(ctx, search.PublicAlias, env.Index); err != nil {
		t.Fatalf("set public alias: %v", err)
	}
	env.runIdle(t)
	rebuild := beginMirrorRebuild(t, env)
	rebuild = runRebuildSlicesUntil(t, env, func(current searchdomain.Rebuild) bool {
		return current.State == searchdomain.RebuildSwitching && !current.Paused
	})
	nodeID := uuid.Must(uuid.NewV7())
	proxy.targetIn(nodeID, mirrorFirstOrdinal, rebuild.TargetIndex)
	env.putNode(t, nodeID, strings.Repeat(timingText, 40))
	firstAbandoned, abandoned := awaitAbandonedWrite(t, env, proxy, time.Now().Add(mirrorSwitchDeadline))
	if !abandoned {
		t.Fatalf("the worker did not abandon the delayed write of page %d to %s", mirrorFirstOrdinal, rebuild.TargetIndex)
	}
	proxy.targetIn(nodeID, mirrorSecondOrdinal, rebuild.TargetIndex)
	bound := firstAbandoned.Add(mirrorFreshnessBound)
	secondAbandoned, abandoned := awaitAbandonedWrite(t, env, proxy, bound)
	if !abandoned {
		t.Fatalf("serving index %s lacked pages of node %s at %s, %s after the mirror write of page %d was abandoned: %s",
			env.Index, nodeID, bound, mirrorFreshnessBound, mirrorFirstOrdinal, indexMismatch(t, env, nodeID))
	}

	bound = secondAbandoned.Add(mirrorFreshnessBound)
	if !runSlicesUntil(t, env, bound, func() bool { return indexMismatch(t, env, nodeID) == "" }) {
		t.Fatalf("serving index %s lacked pages of node %s at %s, %s after the mirror write was abandoned: %s",
			env.Index, nodeID, bound, mirrorFreshnessBound, indexMismatch(t, env, nodeID))
	}

	requireSwitchAfterCopies(t, env, rebuild.TargetIndex)
	replacement := *env
	replacement.Index = rebuild.TargetIndex
	if mismatch := indexMismatch(t, &replacement, nodeID); mismatch != "" {
		t.Fatalf("replacement index %s at the switch: %s", rebuild.TargetIndex, mismatch)
	}
	requireResendAfterHorizon(t, proxy, nodeID, mirrorFirstOrdinal, rebuild.TargetIndex, firstAbandoned)
	requireResendAfterHorizon(t, proxy, nodeID, mirrorSecondOrdinal, rebuild.TargetIndex, secondAbandoned)
}

// requireSwitchAfterCopies runs worker slices until the replacement enters the retiring state.
// The helper reads the replacement state and CopiesPending before each slice.
// It records whether the replacement uses the switching state while a copy item remains pending.
// It fails the test at the end if it never observed that condition.
//
// The helper fails the test if a copy item remains pending at the first observation of the
// retiring state. This observation immediately follows the slice that switches the public alias.
// The helper does not run another slice before this check.
// It then requires the serving index record and the public alias to select target.
func requireSwitchAfterCopies(t *testing.T, env *migrationEnv, target string) {
	t.Helper()
	ctx := integration.WithTestOperation(t, t.Context())
	rebuilds := env.Stores.SearchRebuilds(clock.Wall{})
	waitedWithCopy := false
	runRebuildSlicesUntil(t, env, func(current searchdomain.Rebuild) bool {
		pending, err := rebuilds.CopiesPending(ctx)
		if err != nil {
			t.Fatalf("read pending replacement copies: %v", err)
		}
		if current.State == searchdomain.RebuildSwitching && pending {
			waitedWithCopy = true
		}
		if current.State != searchdomain.RebuildRetiring {
			return false
		}
		if pending {
			t.Fatalf("the public alias moved to %s while a replacement copy was pending", target)
		}
		return true
	})
	if !waitedWithCopy {
		t.Fatal("the replacement never entered the switching state with a pending copy")
	}
	if serving, err := env.Stores.ServingSearchIndex(ctx); err != nil || serving != target {
		t.Fatalf("serving index = %q, error = %v, want %q", serving, err, target)
	}
	if alias, err := env.Adapter.AliasTarget(ctx, search.PublicAlias); err != nil || alias != target {
		t.Fatalf("public alias target = %q, error = %v, want %q", alias, err, target)
	}
}

// requireResendAfterHorizon fails the test if proxy receives fewer than two writes
// of the page for nodeID and ordinal to index. It also fails the test if the second write
// arrives before abandonedAt plus mirrorWriteHorizon minus abandonClockSkew.
func requireResendAfterHorizon(t *testing.T, proxy *pageDelayProxy, nodeID uuid.UUID, ordinal uint64, index string, abandonedAt time.Time) {
	t.Helper()
	received := make([]time.Time, 0, 2)
	shown := make([]string, 0, 2)
	for _, write := range pageWrites(proxy, nodeID, ordinal) {
		if write.index == index {
			received = append(received, write.received)
			shown = append(shown, write.received.UTC().Format(time.RFC3339Nano))
		}
	}
	deadline := abandonedAt.Add(mirrorWriteHorizon - abandonClockSkew)
	if len(received) < 2 || received[1].Before(deadline) {
		t.Fatalf("the replacement index received writes of page %d at %v, want the abandoned write and a resend at or after %s",
			ordinal, shown, deadline.UTC().Format(time.RFC3339Nano))
	}
}
