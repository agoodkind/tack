package integration_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// pacedAccessNodes sets the access backlog size for the hub and its
	// linked nodes. Each item finishes in one short slice using only
	// FoundationDB because the opaque link does not change compiled access.
	pacedAccessNodes = 240
	// pacedDeletedNodes sets how many indexed nodes the test deletes to
	// schedule page cleanup.
	pacedDeletedNodes = 60
	// pacedLiveNodes sets how many new nodes schedule live work.
	pacedLiveNodes = 8
	// pacedLiveRepeats sets how often each live node repeats timingText.
	// Each node has many pages and remains pending during the run.
	pacedLiveRepeats = 400
	// pacedPageDelay sets the proxy delay for each content page write,
	// making live slices slow.
	pacedPageDelay = 150 * time.Millisecond
	// pacedOtherDelay sets the proxy delay for each access update and
	// page retirement, setting their minimum request duration.
	pacedOtherDelay = 40 * time.Millisecond
	// pacedSliceBudget limits each slice's worker time and is shorter
	// than pacedPageDelay. A live slice writes one delayed page before
	// it yields.
	pacedSliceBudget = 100 * time.Millisecond
	// pacedSliceLimit sets the maximum number of claimed slices.
	pacedSliceLimit = 600
	// pacedDeadline sets the elapsed time limit for the slice loop.
	pacedDeadline = 3 * time.Minute
	// shareTolerance sets the largest allowed difference between a
	// measured worker time share and its class weight share.
	shareTolerance = 0.1
	// shareWindowSlices sets the minimum measured window duration as a
	// multiple of the longest slice duration.
	shareWindowSlices = 10
)

// TestSearchWorkerSharesTimeByClassWeight runs one worker with production
// class weights on live, access, and cleanup backlogs through a pacing
// proxy. Page writes wait pacedPageDelay, and access updates and retirements
// wait pacedOtherDelay, making live slices several times longer. While all
// classes remain pending, each must receive its weight share of measured
// worker time within shareTolerance. Access and cleanup must finish while
// live work remains, and the oldest pending access age must stop rising.
// A turn-based class schedule fails this test because slow live slices use
// most worker time and access remains pending at pacedSliceLimit.
func TestSearchWorkerSharesTimeByClassWeight(t *testing.T) {
	env, proxy := newPacedMigrationEnv(t, timingPageBytes)
	buildFairnessBacklog(t, env, pacedAccessNodes, pacedDeletedNodes, pacedLiveNodes, strings.Repeat(timingText, pacedLiveRepeats))
	proxy.pace(pacedPageDelay, pacedOtherDelay)
	slices := runFairnessSlices(t, newFairnessWorker(t, env, pacedSliceBudget), pacedSliceLimit, pacedDeadline)
	requireTimeShares(t, allPendingWindow(slices))
	requireAccessCaughtUp(t, slices)
}

// allPendingWindow returns slices through the first sample after which
// access, cleanup, or live lacks age keys. It returns every slice if all
// three classes remain pending.
func allPendingWindow(slices []fairnessSlice) []fairnessSlice {
	for position, slice := range slices {
		if !slice.accessPending || !slice.cleanupPending || !slice.livePending {
			return slices[:position+1]
		}
	}
	return slices
}

// requireTimeShares sums each class's slice durations and requires the
// window to last at least shareWindowSlices times the longest slice.
// It requires each live, access, and cleanup time share to match its weight
// divided by their combined weights within shareTolerance.
func requireTimeShares(t *testing.T, window []fairnessSlice) {
	t.Helper()
	settings, err := config.LoadSearchWorkerSettings(integration.WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatalf("load search worker settings: %v", err)
	}
	classes := []searchdomain.WorkClass{searchdomain.WorkClassLive, searchdomain.WorkClassAccess, searchdomain.WorkClassCleanup}
	spent := map[string]time.Duration{}
	counts := map[string]int{}
	total, longest := time.Duration(0), time.Duration(0)
	for _, slice := range window {
		spent[slice.class] += slice.duration
		counts[slice.class]++
		total += slice.duration
		longest = max(longest, slice.duration)
	}
	if total < shareWindowSlices*longest {
		t.Fatalf("the slices before the first class finished took %s with a longest slice of %s, want at least %d longest slices; slices by class %v, time by class %v",
			total, longest, shareWindowSlices, counts, spent)
	}
	weights := 0
	for _, class := range classes {
		weights += settings.ClassWeights[string(class)]
	}
	for _, class := range classes {
		share := float64(spent[string(class)]) / float64(total)
		want := float64(settings.ClassWeights[string(class)]) / float64(weights)
		if math.Abs(share-want) > shareTolerance {
			t.Fatalf("class %s received %.2f of %s of worker time across %d slices, want %.2f within %.2f; time by class %v",
				class, share, total, len(window), want, shareTolerance, spent)
		}
	}
}
