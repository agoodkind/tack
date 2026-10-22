package integration_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/searchaccess"
	"goodkind.io/tack/internal/service"
)

const (
	// rolloutStepDeadline bounds one wait for a rollout state.
	rolloutStepDeadline = 10 * time.Minute
	// rolloutIdlePause sets the delay after a slice does not claim work.
	rolloutIdlePause = 250 * time.Millisecond
	// rolloutDrainSlices bounds the worker slices in one drain.
	rolloutDrainSlices = 2000
	// rolloutRejectedWait sets the wait after OpenSearch rejects a slice with
	// HTTP 429. The wait covers the 10 second retry delay of released work.
	rolloutRejectedWait = 15 * time.Second
	// rolloutRejectionText is the worker error text for an OpenSearch HTTP
	// 429 rejection. The ML Commons memory circuit breaker returns this
	// status near the heap limit.
	rolloutRejectionText = "returned status 429 "
)

// bothAccessVersions lists the sorted write versions for a rollout from
// the stable policy to the rotated policy.
var bothAccessVersions = []string{searchaccess.StableVersion, searchaccess.RotatedVersion}

// beginAccessRollout starts a rollout to the rotated version through the
// production rollout store.
func beginAccessRollout(t *testing.T, fixture rolloutFixture, authorityID uuid.UUID) searchdomain.AccessRollout {
	t.Helper()
	current := currentAccessRollout(t, fixture, authorityID)
	begun, err := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet()).Begin(t.Context(), searchdomain.BeginAccessRollout{
		AuthorityID: authorityID, CandidateVersion: searchaccess.RotatedVersion, ExpectedGeneration: current.Generation,
	})
	if err != nil {
		t.Fatalf("cannot begin rollout for authority %s: %v", authorityID, err)
	}
	return begun
}

// currentAccessRollout reads the rollout of authorityID.
func currentAccessRollout(t *testing.T, fixture rolloutFixture, authorityID uuid.UUID) searchdomain.AccessRollout {
	t.Helper()
	rollout, err := fixture.stores.SearchRollouts(clock.Wall{}, fixture.stores.SearchPolicySet()).Current(t.Context(), authorityID)
	if err != nil {
		t.Fatalf("cannot read rollout for authority %s: %v", authorityID, err)
	}
	return rollout
}

// requireAccessRollout requires the specified active version, write
// versions, and phase.
func requireAccessRollout(t *testing.T, rollout searchdomain.AccessRollout, active string, writes []string, phase searchdomain.AccessPhase) {
	t.Helper()
	if rollout.ActiveVersion != active || !slices.Equal(rollout.WriteVersions, writes) || rollout.Phase != phase {
		t.Fatalf("rollout %+v must use active version %s, write versions %v, and phase %s", rollout, active, writes, phase)
	}
}

// runSliceOrWait runs one worker slice, waits after an engine rejection,
// and pauses when the slice does not claim work.
func runSliceOrWait(t *testing.T, worker *service.SearchWorker) {
	t.Helper()
	claimed, err := worker.RunSlice(t.Context())
	if err != nil {
		waitAfterRejection(t, err)
		return
	}
	if !claimed {
		time.Sleep(rolloutIdlePause)
	}
}

// waitAfterRejection fails the test unless err is an OpenSearch HTTP 429
// rejection, then waits for rolloutRejectedWait.
func waitAfterRejection(t *testing.T, err error) {
	t.Helper()
	if !strings.Contains(err.Error(), rolloutRejectionText) {
		t.Fatalf("worker slice failed: %v", err)
	}
	time.Sleep(rolloutRejectedWait)
}

// runRolloutUntil runs fixture worker slices until done returns true for
// the rollout and returns that state.
func runRolloutUntil(t *testing.T, fixture rolloutFixture, authorityID uuid.UUID, done func(searchdomain.AccessRollout) bool) searchdomain.AccessRollout {
	t.Helper()
	return runWorkerUntil(t, fixture, fixture.worker, authorityID, done)
}

// runWorkerUntil runs slices of worker until done returns true for the
// rollout of authorityID and returns that state.
func runWorkerUntil(t *testing.T, fixture rolloutFixture, worker *service.SearchWorker, authorityID uuid.UUID, done func(searchdomain.AccessRollout) bool) searchdomain.AccessRollout {
	t.Helper()
	deadline := time.Now().Add(rolloutStepDeadline)
	for time.Now().Before(deadline) {
		current := currentAccessRollout(t, fixture, authorityID)
		if done(current) {
			return current
		}
		runSliceOrWait(t, worker)
	}
	t.Fatalf("rollout for authority %s did not enter the expected state within %s", authorityID, rolloutStepDeadline)
	return searchdomain.AccessRollout{}
}

// rolloutStable reports whether the rollout returned to the stable phase.
func rolloutStable(rollout searchdomain.AccessRollout) bool {
	return rollout.Phase == searchdomain.AccessStable
}

// drainRolloutWork runs slices until the worker cannot claim work.
func drainRolloutWork(t *testing.T, fixture rolloutFixture) {
	t.Helper()
	for range rolloutDrainSlices {
		claimed, err := fixture.worker.RunSlice(t.Context())
		if err != nil {
			waitAfterRejection(t, err)
			continue
		}
		if !claimed {
			return
		}
	}
	t.Fatalf("work did not drain within %d slices", rolloutDrainSlices)
}

// drainLiveWork claims and processes live work until the worker cannot
// claim more, without running rollout steps.
func drainLiveWork(t *testing.T, fixture rolloutFixture) {
	t.Helper()
	store := fixture.stores.SearchWork(clock.Wall{})
	for range rolloutDrainSlices {
		work, err := store.Claim(t.Context(), searchdomain.WorkClassLive, "rollout-live-driver", time.Minute)
		if errors.Is(err, searchdomain.ErrNoWork) {
			return
		}
		if err != nil {
			t.Fatalf("cannot claim live work: %v", err)
		}
		if err := fixture.worker.Process(t.Context(), work); err != nil {
			t.Fatalf("cannot process live work for node %s: %v", work.NodeID, err)
		}
	}
	t.Fatalf("work did not drain within %d slices", rolloutDrainSlices)
}

// expectedAccessKeys compiles and sorts nodeID's access keys under both
// versions through the production policy set.
func expectedAccessKeys(t *testing.T, fixture rolloutFixture, orgID, nodeID uuid.UUID) []string {
	t.Helper()
	keys := make([]string, 0, len(bothAccessVersions))
	for _, version := range bothAccessVersions {
		access, err := fixture.stores.SearchPolicySet().Index(t.Context(), searchaccess.IndexAccessRequest{
			Version: version, OrganizationID: orgID, ResourceID: nodeID, Generation: 0,
		})
		if err != nil {
			t.Fatalf("cannot compile access version %s for node %s: %v", version, nodeID, err)
		}
		keys = append(keys, access.Keys...)
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}
