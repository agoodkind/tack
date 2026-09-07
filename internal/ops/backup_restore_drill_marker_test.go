package ops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// flakyMarkerStore is the backup bucket on a bad day: it refuses the first
// failures puts the way an object store answering 500 does, then behaves. It
// keeps the production writer on the far side of the put so what lands is the
// JSON the staleness check reads.
type flakyMarkerStore struct {
	store    *markerStore
	failures int
	puts     int
}

func (s *flakyMarkerStore) put(key string, body []byte) error {
	s.puts++
	if s.puts <= s.failures {
		return errors.New("put object tack-backups/" + key + ": operation error S3: PutObject, https response error StatusCode: 500")
	}
	return s.store.put(key, body)
}

// recordMarkerWaits swaps the package pause for one that records each requested
// pause and reports whether the context still stands, so a test proves the
// backoff without taking it.
func recordMarkerWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	waitFunc = func(ctx context.Context, d time.Duration) bool {
		waits = append(waits, d)
		return ctx.Err() == nil
	}
	t.Cleanup(func() { waitFunc = waitUntil })
	return &waits
}

// TestRestoreDrillMarkerRetriesAFailedPut proves a marker put that fails and
// then recovers still lands, dated to when the drill passed rather than to the
// attempt that landed, after a backoff that doubles between attempts.
func TestRestoreDrillMarkerRetriesAFailedPut(t *testing.T) {
	passedAt := time.Date(2026, 9, 5, 3, 30, 0, 0, time.UTC)
	nowFunc = func() time.Time { return passedAt }
	t.Cleanup(func() { nowFunc = time.Now })
	waits := recordMarkerWaits(t)
	store := &flakyMarkerStore{store: newMarkerStore(), failures: 3, puts: 0}

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
	marker, found, err := readBackupStatusMarker(context.Background(), store.store.get, backupStalenessRehearsalName)
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

// TestRestoreDrillMarkerGivesUpAfterEveryAttemptFails proves a store that
// never accepts the put fails the drill with the marker named as the failed
// step, after exactly the bounded attempts and their pauses, and writes
// nothing the staleness check could mistake for a rehearsal.
func TestRestoreDrillMarkerGivesUpAfterEveryAttemptFails(t *testing.T) {
	waits := recordMarkerWaits(t)
	store := &flakyMarkerStore{store: newMarkerStore(), failures: restoreDrillMarkerAttempts + 1, puts: 0}

	err := recordRestoreDrillRehearsal(context.Background(), store.put, "rt20260906T033000Z-7",
		[]string{"fdb", "yugabyte"})
	if err == nil {
		t.Fatal("a marker that never lands must fail the drill")
	}
	if !strings.HasPrefix(err.Error(), "restore-drill: every leg passed but the rehearsal marker did not land: ") {
		t.Fatalf("the error must name the marker as the failed step, got: %v", err)
	}
	if !strings.Contains(err.Error(), "StatusCode: 500") {
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
	if _, found, _ := readBackupStatusMarker(context.Background(), store.store.get, backupStalenessRehearsalName); found {
		t.Fatal("no marker may land when every put was refused")
	}
}

// TestRestoreDrillMarkerStopsWhenTheContextEnds proves a drill interrupted
// between attempts fails at once with the marker named, rather than taking the
// remaining pauses against a context that has already ended.
func TestRestoreDrillMarkerStopsWhenTheContextEnds(t *testing.T) {
	waits := recordMarkerWaits(t)
	store := &flakyMarkerStore{store: newMarkerStore(), failures: restoreDrillMarkerAttempts + 1, puts: 0}
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
