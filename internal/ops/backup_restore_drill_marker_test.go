package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// countingMarkerStore puts markers into the test object store and counts the
// attempts, so a test can stop the engine under the drill and see each put the
// drill makes fail for real.
type countingMarkerStore struct {
	store *backupTestStore
	puts  int
}

func (s *countingMarkerStore) put(key string, body []byte) error {
	s.puts++
	return s.store.putBytes(key, body)
}

// recordMarkerWaits swaps the package pause for one that records each requested
// pause, runs onWait with the number of pauses so far, and reports whether the
// context still stands, so a test proves the backoff without taking it.
func recordMarkerWaits(t *testing.T, onWait func(waits int)) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	waitFunc = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		onWait(len(waits))
		return ctx.Err() == nil
	}
	t.Cleanup(func() { waitFunc = waitUntil })
	return &waits
}

// noMarkerWaitAction leaves the engine as it is during a pause.
func noMarkerWaitAction(int) {}

// TestRestoreDrillMarkerRetriesAFailedPut stops the object store under the
// drill and starts it again during the third pause, and proves the marker
// still lands, dated to when the drill passed rather than to the attempt that
// landed, after a backoff that doubles between attempts.
func TestRestoreDrillMarkerRetriesAFailedPut(t *testing.T) {
	passedAt := time.Date(2026, 9, 5, 3, 30, 0, 0, time.UTC)
	nowFunc = func() time.Time { return passedAt }
	t.Cleanup(func() { nowFunc = time.Now })
	store := &countingMarkerStore{store: newBackupTestStore(t, nil), puts: 0}
	store.store.stop()
	defer store.store.ensureStarted()
	waits := recordMarkerWaits(t, func(waits int) {
		if waits == 3 {
			store.store.start()
		}
	})

	err := recordRestoreDrillRehearsal(context.Background(), store.put, "rt20260905T033000Z-7",
		[]string{"fdb", "yugabyte"})
	if err != nil {
		t.Fatalf("a put that recovers within the attempts must land: %v", err)
	}
	if store.puts != 4 {
		t.Fatalf("puts = %d, want 3 failures and the one that landed", store.puts)
	}
	wantWaits := []time.Duration{8 * time.Second, 16 * time.Second, 32 * time.Second}
	if len(*waits) != len(wantWaits) {
		t.Fatalf("waits = %v, want %v", *waits, wantWaits)
	}
	for i, want := range wantWaits {
		if (*waits)[i] != want {
			t.Fatalf("waits = %v, want %v", *waits, wantWaits)
		}
	}
	marker, found, err := readBackupStatusMarker(context.Background(), store.store.getBytes, backupStalenessRehearsalName)
	if err != nil || !found {
		t.Fatalf("the check must find the marker: found=%v err=%v", found, err)
	}
	if !marker.At.Equal(passedAt) {
		t.Fatalf("marker at = %s, want the drill's pass time %s", marker.At, passedAt)
	}
	if marker.Detail != "restore drill rt20260905T033000Z-7 passed: fdb, yugabyte" {
		t.Fatalf("marker detail = %q", marker.Detail)
	}
}

// TestRestoreDrillMarkerGivesUpAfterEveryAttemptFails proves a store that is
// down for every attempt fails the drill with the marker named as the failed
// step, after exactly the bounded attempts and their pauses, and writes
// nothing the staleness check could mistake for a rehearsal.
func TestRestoreDrillMarkerGivesUpAfterEveryAttemptFails(t *testing.T) {
	waits := recordMarkerWaits(t, noMarkerWaitAction)
	store := &countingMarkerStore{store: newBackupTestStore(t, nil), puts: 0}
	store.store.stop()
	defer store.store.ensureStarted()

	err := recordRestoreDrillRehearsal(context.Background(), store.put, "rt20260906T033000Z-7",
		[]string{"fdb", "yugabyte"})
	if err == nil {
		t.Fatal("a marker that never lands must fail the drill")
	}
	if !strings.HasPrefix(err.Error(), "restore-drill: every leg passed but the rehearsal marker did not land: ") {
		t.Fatalf("the error must name the marker as the failed step, got: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("the error must carry the store's refusal, got: %v", err)
	}
	var coded interface{ OperatorExitStatus() int }
	if !errors.As(err, &coded) || coded.OperatorExitStatus() != restoreDrillMarkerExitStatus {
		t.Fatalf("a marker-only failure must declare exit status %d, got: %#v", restoreDrillMarkerExitStatus, err)
	}
	if store.puts != restoreDrillMarkerAttempts {
		t.Fatalf("puts = %d, want every one of the %d attempts and no more", store.puts, restoreDrillMarkerAttempts)
	}
	if len(*waits) != restoreDrillMarkerAttempts-1 {
		t.Fatalf("waits = %v, want one pause between each pair of attempts", *waits)
	}
	store.store.start()
	if _, found, _ := readBackupStatusMarker(context.Background(), store.store.getBytes, backupStalenessRehearsalName); found {
		t.Fatal("no marker may land when every put was refused")
	}
}

// TestRestoreDrillMarkerStopsWhenTheContextEnds proves a drill interrupted
// between attempts fails at once with the marker named, rather than taking the
// remaining pauses against a context that has already ended.
func TestRestoreDrillMarkerStopsWhenTheContextEnds(t *testing.T) {
	waits := recordMarkerWaits(t, noMarkerWaitAction)
	store := &countingMarkerStore{store: newBackupTestStore(t, nil), puts: 0}
	store.store.stop()
	defer store.store.ensureStarted()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := recordRestoreDrillRehearsal(ctx, store.put, "rt20260906T033000Z-8", []string{"fdb", "yugabyte"})
	if err == nil || !strings.Contains(err.Error(), "the rehearsal marker did not land") {
		t.Fatalf("an interrupted drill must still name the marker, got: %v", err)
	}
	if store.puts != 1 || len(*waits) != 1 {
		t.Fatalf("puts = %d waits = %d, want one attempt and one refused pause", store.puts, len(*waits))
	}
}
