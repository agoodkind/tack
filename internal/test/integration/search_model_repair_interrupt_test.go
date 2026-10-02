package integration

import (
	"context"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

// TestSearchModelRepairInterruptedAttempt drives the uncount of an
// interrupted repair attempt through the real FoundationDB store. The
// uncount removes the attempt and keeps its claim time. An uncount for an
// older claim, an uncount after a reset, and an uncount past its deadline
// change nothing.
func TestSearchModelRepairInterruptedAttempt(t *testing.T) {
	store := newSearchStore(t).SearchModelRepairs()
	modelID := opaqueSearchKey("m")
	start := clock.Now().UTC()

	interrupted := claimRepair(t, store, modelID, start)
	requireClaim(t, "first claim", interrupted, true, 1)
	requireUncount(t, store, "interrupted claim", t.Context(), interrupted, true)
	requireClaim(t, "claim inside the kept spacing", claimRepair(t, store, modelID, start.Add(10*time.Second)), false, 0)

	newer := claimRepair(t, store, modelID, start.Add(46*time.Second))
	requireClaim(t, "claim after the spacing", newer, true, 1)
	if !newer.Record.EpisodeStarted.Equal(start) {
		t.Fatalf("claim after the uncount started episode %s, want the episode from %s", newer.Record.EpisodeStarted, start)
	}
	requireUncount(t, store, "older claim after a newer attempt", t.Context(), interrupted, false)

	expired, cancel := context.WithDeadline(context.Background(), clock.Now().Add(-time.Second))
	defer cancel()
	if uncounted, err := store.UncountModelRepair(expired, newer); err == nil || uncounted {
		t.Fatalf("uncount past its deadline returned %t err %v, want an error", uncounted, err)
	}
	requireUncount(t, store, "newer claim after the expired write", t.Context(), newer, true)

	if _, found, err := store.ResetModelRepair(t.Context()); err != nil || !found {
		t.Fatalf("reset found %t err %v, want the record", found, err)
	}
	requireUncount(t, store, "claim after a reset", t.Context(), newer, false)
}

func requireUncount(t *testing.T, store *fdbadapter.SearchModelRepairStore, step string, ctx context.Context, claim searchdomain.ModelRepairClaim, want bool) {
	t.Helper()
	uncounted, err := store.UncountModelRepair(ctx, claim)
	if err != nil || uncounted != want {
		t.Fatalf("%s: uncount returned %t err %v, want %t", step, uncounted, err, want)
	}
}
