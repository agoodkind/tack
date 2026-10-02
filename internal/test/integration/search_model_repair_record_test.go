package integration

import (
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

const (
	// repairRecordSpacing is the attempt spacing of the record test.
	repairRecordSpacing = 45 * time.Second
	// repairRecordMaxAttempts is the attempt limit of the record test.
	repairRecordMaxAttempts = 3
)

// TestSearchModelRepairRecord drives the model repair record through the real
// FoundationDB store. The claim refuses an attempt inside the spacing and a
// fourth attempt, a released claim does not count, a claim for another model
// starts a new episode, and a reset deletes the record.
func TestSearchModelRepairRecord(t *testing.T) {
	store := newSearchStore(t).SearchModelRepairs()
	modelID, otherModelID := opaqueSearchKey("m"), opaqueSearchKey("m")
	start := clock.Now().UTC()

	first := claimRepair(t, store, modelID, start)
	requireClaim(t, "first claim", first, true, 1)
	if first.PreviousFound || !first.Record.EpisodeStarted.Equal(start) {
		t.Fatalf("first claim previous found %t, episode start %s, want a new episode at %s", first.PreviousFound, first.Record.EpisodeStarted, start)
	}
	requireClaim(t, "claim inside the spacing", claimRepair(t, store, modelID, start.Add(10*time.Second)), false, 1)

	aborted := claimRepair(t, store, modelID, start.Add(46*time.Second))
	requireClaim(t, "second claim", aborted, true, 2)
	if err := store.ReleaseModelRepair(t.Context(), aborted); err != nil {
		t.Fatalf("release aborted claim: %v", err)
	}
	second := claimRepair(t, store, modelID, start.Add(47*time.Second))
	requireClaim(t, "claim after the release", second, true, 2)
	requireClaim(t, "third claim", claimRepair(t, store, modelID, start.Add(100*time.Second)), true, 3)
	requireClaim(t, "fourth claim", claimRepair(t, store, modelID, start.Add(200*time.Second)), false, 3)

	replaced := claimRepair(t, store, otherModelID, start.Add(201*time.Second))
	requireClaim(t, "claim for another model", replaced, true, 1)
	if replaced.Replaced != modelID || replaced.PreviousFound {
		t.Fatalf("claim for another model replaced %q with previous found %t, want %q without a previous record", replaced.Replaced, replaced.PreviousFound, modelID)
	}

	record, found, err := store.ResetModelRepair(t.Context())
	if err != nil || !found || record.ModelID != otherModelID || record.Attempts != 1 {
		t.Fatalf("reset returned %+v found %t err %v, want model %s with 1 attempt", record, found, err, otherModelID)
	}
	if _, found, err := store.ResetModelRepair(t.Context()); err != nil || found {
		t.Fatalf("second reset found %t err %v, want no record", found, err)
	}
	requireClaim(t, "claim after the reset", claimRepair(t, store, modelID, start.Add(202*time.Second)), true, 1)
}

func claimRepair(t *testing.T, store *fdbadapter.SearchModelRepairStore, modelID string, now time.Time) searchdomain.ModelRepairClaim {
	t.Helper()
	claim, err := store.ClaimModelRepair(t.Context(), searchdomain.ModelRepairRequest{
		ModelID: modelID, Now: now, Spacing: repairRecordSpacing, MaxAttempts: repairRecordMaxAttempts,
	})
	if err != nil {
		t.Fatalf("claim repair of model %s at %s: %v", modelID, now, err)
	}
	return claim
}

func requireClaim(t *testing.T, step string, claim searchdomain.ModelRepairClaim, granted bool, attempts int) {
	t.Helper()
	if claim.Granted != granted || claim.Record.Attempts != attempts {
		t.Fatalf("%s: granted %t with %d attempts, want granted %t with %d attempts", step, claim.Granted, claim.Record.Attempts, granted, attempts)
	}
}
