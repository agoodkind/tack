package integration_test

import (
	"strings"
	"testing"
	"time"

	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// catchUpPageBytes sets the production page size. Each full live page
	// runs real sparse inference.
	catchUpPageBytes = 4096
	// catchUpAccessNodes sets the access backlog size, including the hub
	// and its linked nodes.
	catchUpAccessNodes = 24
	// catchUpDeletedNodes sets how many indexed nodes the test deletes
	// to schedule page cleanup.
	catchUpDeletedNodes = 20
	// catchUpLiveNodes sets how many new nodes schedule live work.
	catchUpLiveNodes = 48
	// catchUpLiveSentence supplies the sentence each live node repeats.
	catchUpLiveSentence = "catch up backlog page with enough distinct words for real sparse inference "
	// catchUpLiveRepeats sets the sentence count for each live node.
	// Each node fills several pages without exceeding FoundationDB's
	// value size limit.
	catchUpLiveRepeats = 520
	// catchUpSliceBudget sets the production worker slice budget.
	catchUpSliceBudget = 2 * time.Second
	// catchUpSliceLimit sets the maximum number of claimed slices.
	catchUpSliceLimit = 120
	// catchUpDeadline sets the elapsed time limit for the slice loop.
	catchUpDeadline = 8 * time.Minute
)

// TestSearchCatchUpFinishesAccessAndCleanupBeforeLive runs one worker with
// production class weights and slice budget on full live pages with real
// sparse inference, access work for a hub and linked nodes, and cleanup of
// deleted indexed nodes. Access and cleanup must finish within
// catchUpSliceLimit slices while live work remains. The worker must run
// each of these classes at least once. A turn-based class schedule fails
// this test because access receives too few slices to finish its backlog
// before the slice limit.
func TestSearchCatchUpFinishesAccessAndCleanupBeforeLive(t *testing.T) {
	env := newMigrationEnv(t, catchUpPageBytes)
	liveText := strings.Repeat(catchUpLiveSentence, catchUpLiveRepeats)
	buildFairnessBacklog(t, env, catchUpAccessNodes, catchUpDeletedNodes, catchUpLiveNodes, liveText)
	slices := runFairnessSlices(t, newFairnessWorker(t, env, catchUpSliceBudget), catchUpSliceLimit, catchUpDeadline)
	requireAccessCaughtUp(t, slices)
	claimed := map[string]int{}
	for _, slice := range slices {
		claimed[slice.class]++
	}
	for _, class := range []searchdomain.WorkClass{searchdomain.WorkClassLive, searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup} {
		if claimed[string(class)] == 0 {
			t.Fatalf("the worker ran %d slices by class %v without a %s slice", len(slices), claimed, class)
		}
	}
}
