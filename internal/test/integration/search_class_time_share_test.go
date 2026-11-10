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
	//
	// In the failed CI run, the worker finished 240 access items in a
	// 2.633 s window, about 11 ms per item or 91 items per window second.
	// With shareWindowSlices = 10, the 265 ms longest slice requires a
	// window of 10 * 265 ms = 2.65 s, or about 241 items at that rate.
	// At that rate, 720 items give about 7.9 s of window. That window
	// meets the minimum for a longest slice up to about 790 ms, three
	// times the longest CI slice.
	//
	// The window ends when access or cleanup finishes first. While all three
	// classes have pending work, access and cleanup each get 1/4 of worker time.
	// At CI rates, 60 cleanup items need about 3.4 s of worker time and 13.7 s
	// of window time; access finishes first. If cleanup finishes first on a
	// loaded host, the window lasts about four times the cleanup worker time.
	pacedAccessNodes = 720
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
	// Each access item and each cleanup item uses one slice, for
	// 720 + 60 = 780 slices. Each live slice takes about 260 ms.
	// A local run with 240 access items claimed 27 live slices in
	// 11.2 s of worker time. A run with 720 access items needs fewer
	// than 100 live slices.
	//
	// A schedule that gives each class a turn allows access at most
	// one third of 1200 slices, or 400 slices. Access still has pending
	// work at that limit.
	pacedSliceLimit = 1200
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
	run := sliceTotalsOf(slices)
	t.Logf("the run claimed %d slices that took %s with a longest slice of %s; slices by class %v, time by class %v",
		len(slices), run.total, run.longest, run.counts, run.spent)
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

// sliceTotals stores duration totals and counts for fairnessSlice values.
// The spent field stores worker time by class, and counts stores slice
// counts by class. The total field stores the summed slice durations,
// and longest stores the longest slice duration.
type sliceTotals struct {
	spent          map[string]time.Duration
	counts         map[string]int
	total, longest time.Duration
}

// sliceTotalsOf sums durations and counts slices by class for the
// fairnessSlice values. It also sums all slice durations and finds
// the longest slice duration. It returns these values as a sliceTotals.
func sliceTotalsOf(slices []fairnessSlice) sliceTotals {
	totals := sliceTotals{spent: map[string]time.Duration{}, counts: map[string]int{}, total: 0, longest: 0}
	for _, slice := range slices {
		totals.spent[slice.class] += slice.duration
		totals.counts[slice.class]++
		totals.total += slice.duration
		totals.longest = max(totals.longest, slice.duration)
	}
	return totals
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
	measured := sliceTotalsOf(window)
	spent, total, longest := measured.spent, measured.total, measured.longest
	t.Logf("the slices before the first class finished took %s with a longest slice of %s; slices by class %v, time by class %v",
		total, longest, measured.counts, spent)
	if total < shareWindowSlices*longest {
		t.Fatalf("the slices before the first class finished took %s with a longest slice of %s, want at least %d longest slices; slices by class %v, time by class %v",
			total, longest, shareWindowSlices, measured.counts, spent)
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
