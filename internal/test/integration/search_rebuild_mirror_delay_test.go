package integration_test

import (
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

// TestSearchRebuildMirrorDelayKeepsServingFresh calls runTwoMirrorMisses without a step
// between the misses.
// The test calls requireSwitchAfterCopies.
// The test requires the replacement index to store every page of the node at the current revision.
// For pages 1 and 3, the test requires a second replacement index write at or after that page's
// abandonment time plus mirrorWriteHorizon minus one second of clock skew.
// runTwoMirrorMisses fails if the serving cursor waits for page 1's 60-second pending deadline.
func TestSearchRebuildMirrorDelayKeepsServingFresh(t *testing.T) {
	run := runTwoMirrorMisses(t, mirrorSteps{duringFirstHold: nil, afterFirstMiss: nil})
	requireSwitchAfterCopies(t, run.env, run.target)
	requireReplacementPages(t, run)
	requireResendAfterHorizon(t, run.proxy, run.nodeID, mirrorFirstOrdinal, run.target, run.firstAbandoned)
	requireResendAfterHorizon(t, run.proxy, run.nodeID, mirrorSecondOrdinal, run.target, run.secondAbandoned)
}

// TestSearchRebuildMirrorCopyKeepsOwedPagesAcrossGenerations calls runTwoMirrorMisses
// with raiseNodeGeneration between the misses.
// The copy item from page 1 waits for the page 1 deadline when raiseNodeGeneration runs.
// raiseNodeGeneration assigns the copy work record a new generation and leaves the copy
// checkpoint at the old generation.
// The second miss then schedules the copy for page 3.
// The test calls requireSwitchAfterCopies and requires the replacement index to store
// every page of the node at the switch.
// A copy that starts at page 3 does not write page 1.
// The replacement index then lacks page 1.
func TestSearchRebuildMirrorCopyKeepsOwedPagesAcrossGenerations(t *testing.T) {
	run := runTwoMirrorMisses(t, mirrorSteps{duringFirstHold: nil, afterFirstMiss: raiseNodeGeneration})
	requireSwitchAfterCopies(t, run.env, run.target)
	requireReplacementPages(t, run)
}

// requireReplacementPages fails the test unless the replacement index of run stores one
// active page for every reader page of the node.
// Each active page must match the reader page's text, ordinal, revision, and projection.
func requireReplacementPages(t *testing.T, run mirrorRun) {
	t.Helper()
	replacement := *run.env
	replacement.Index = run.target
	if mismatch := indexMismatch(t, &replacement, run.nodeID); mismatch != "" {
		t.Fatalf("replacement index %s at the switch: %s", run.target, mismatch)
	}
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

// requireResendAfterHorizon reads the page writes that the proxy received for nodeID, ordinal,
// and index. The first write is the abandoned write, and its node revision identifies the document.
// The helper ignores writes with other revisions of the same ordinal.
// The helper fails the test unless the proxy received a second write of the same document at or after
// abandonedAt plus mirrorWriteHorizon minus abandonClockSkew.
func requireResendAfterHorizon(t *testing.T, proxy *pageDelayProxy, nodeID uuid.UUID, ordinal uint64, index string, abandonedAt time.Time) {
	t.Helper()
	revision := ""
	received := make([]time.Time, 0, 2)
	shown := make([]string, 0, 2)
	for _, write := range pageWrites(proxy, nodeID, ordinal) {
		if write.index != index {
			continue
		}
		if revision == "" {
			revision = write.revision
		}
		if write.revision != revision {
			continue
		}
		received = append(received, write.received)
		shown = append(shown, write.received.UTC().Format(time.RFC3339Nano))
	}
	deadline := abandonedAt.Add(mirrorWriteHorizon - abandonClockSkew)
	if len(received) < 2 || received[1].Before(deadline) {
		t.Fatalf("the replacement index received writes of page %d at revision %q at %v, want the abandoned write and a resend at or after %s",
			ordinal, revision, shown, deadline.UTC().Format(time.RFC3339Nano))
	}
}
