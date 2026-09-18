// backup_fdb_version_log_test.go exercises the version-to-time record through
// the production writer, reader, and lookup, plus the command an operator runs
// after a total loss, over an object store that answers real HTTP.

package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// recordFDBPoints appends each point through the production writer.
func recordFDBPoints(t *testing.T, store *markerStore, points ...fdbRestorablePoint) {
	t.Helper()
	for _, point := range points {
		if err := appendFDBRestorablePoint(context.Background(), store.get, store.put, point); err != nil {
			t.Fatalf("appendFDBRestorablePoint(%d): %v", point.Version, err)
		}
	}
}

// TestFDBVersionLogRoundTripAndLookup is the mapping a total-loss restore
// needs: several readings recorded, then a moment converted to the newest
// version that does not overshoot it.
func TestFDBVersionLogRoundTripAndLookup(t *testing.T) {
	ctx := context.Background()
	store := newMarkerStore()
	base := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	recordFDBPoints(t, store,
		fdbRestorablePoint{Version: 100, At: base},
		fdbRestorablePoint{Version: 200, At: base.Add(10 * time.Minute)},
		fdbRestorablePoint{Version: 300, At: base.Add(20 * time.Minute)},
	)

	log, err := readFDBVersionLog(ctx, store.get)
	if err != nil {
		t.Fatalf("readFDBVersionLog: %v", err)
	}
	if len(log.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(log.Points))
	}

	// A moment between two readings restores to the older one: the newer
	// reading covers mutations the operator asked to exclude.
	point, found := fdbVersionAt(log, base.Add(15*time.Minute))
	if !found || point.Version != 200 {
		t.Fatalf("version at +15m = %d found=%v, want 200", point.Version, found)
	}
	point, found = fdbVersionAt(log, base.Add(time.Hour))
	if !found || point.Version != 300 {
		t.Fatalf("version at +1h = %d found=%v, want the newest reading", point.Version, found)
	}
	if _, found := fdbVersionAt(log, base.Add(-time.Second)); found {
		t.Fatal("a moment before every reading has no version to name")
	}
}

// TestAppendFDBRestorablePointIgnoresARepeatedReading proves two checks that
// see the same restorable point leave one entry, so the record does not grow
// while the backup is idle.
func TestAppendFDBRestorablePointIgnoresARepeatedReading(t *testing.T) {
	ctx := context.Background()
	store := newMarkerStore()
	at := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	recordFDBPoints(t, store,
		fdbRestorablePoint{Version: 100, At: at},
		fdbRestorablePoint{Version: 100, At: at.Add(10 * time.Minute)},
	)

	log, err := readFDBVersionLog(ctx, store.get)
	if err != nil {
		t.Fatalf("readFDBVersionLog: %v", err)
	}
	if len(log.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(log.Points))
	}
	if !log.Points[0].At.Equal(at) {
		t.Fatalf("the first reading's moment must stand, got %s", log.Points[0].At)
	}
}

// TestAppendFDBRestorablePointBoundsTheRecord proves the record drops its
// oldest entries rather than growing without limit.
func TestAppendFDBRestorablePointBoundsTheRecord(t *testing.T) {
	ctx := context.Background()
	store := newMarkerStore()
	base := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	for i := range fdbVersionLogMaxEntries + 5 {
		point := fdbRestorablePoint{Version: int64(i + 1), At: base.Add(time.Duration(i) * time.Minute)}
		if err := appendFDBRestorablePoint(ctx, store.get, store.put, point); err != nil {
			t.Fatalf("appendFDBRestorablePoint(%d): %v", point.Version, err)
		}
	}

	log, err := readFDBVersionLog(ctx, store.get)
	if err != nil {
		t.Fatalf("readFDBVersionLog: %v", err)
	}
	if len(log.Points) != fdbVersionLogMaxEntries {
		t.Fatalf("points = %d, want %d", len(log.Points), fdbVersionLogMaxEntries)
	}
	if log.Points[0].Version != 6 {
		t.Fatalf("oldest kept version = %d, want the five oldest dropped", log.Points[0].Version)
	}
}

// TestReadFDBVersionLogAbsent proves a store that has never recorded a reading
// answers with an empty record rather than an error, because the first check
// has to start somewhere.
func TestReadFDBVersionLogAbsent(t *testing.T) {
	log, err := readFDBVersionLog(context.Background(), newMarkerStore().get)
	if err != nil {
		t.Fatalf("an absent record is a state, not an error: %v", err)
	}
	if len(log.Points) != 0 {
		t.Fatalf("points = %d, want none", len(log.Points))
	}
}

// TestRunBackupFDBRestoreVersionAnswersFromTheObjectStore drives the operator
// command against an object store that answers real HTTP, with no cluster
// anywhere, which is the total-loss case the record exists for.
func TestRunBackupFDBRestoreVersionAnswersFromTheObjectStore(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	store := newMarkerStore()
	recordFDBPoints(t, store,
		fdbRestorablePoint{Version: 100720665, At: base},
		fdbRestorablePoint{Version: 100999999, At: base.Add(10 * time.Minute)},
	)
	_, cfg := newFakeBackupObjectStore(t, "tack-backups", map[string][]byte{
		fdbVersionLogKey: store.objects[fdbVersionLogKey],
	})

	var out bytes.Buffer
	if err := RunBackupFDBRestoreVersion(ctx, cfg, base.Add(5*time.Minute), &out); err != nil {
		t.Fatalf("RunBackupFDBRestoreVersion: %v", err)
	}
	if !strings.Contains(out.String(), "restore to version 100720665") {
		t.Fatalf("output must name the version that does not overshoot:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "2026-09-18T04:00:00Z") {
		t.Fatalf("output must date the version it names:\n%s", out.String())
	}

	out.Reset()
	err := RunBackupFDBRestoreVersion(ctx, cfg, base.Add(-time.Hour), &out)
	if err == nil || !strings.Contains(err.Error(), "oldest recorded reading") {
		t.Fatalf("a moment before the record must be refused with its bound, got %v", err)
	}
}

// TestRunBackupFDBRestoreVersionRefusesAnEmptyRecord proves a store holding no
// readings says so rather than naming a zero version.
func TestRunBackupFDBRestoreVersionRefusesAnEmptyRecord(t *testing.T) {
	_, cfg := newFakeBackupObjectStore(t, "tack-backups", map[string][]byte{})
	var out bytes.Buffer

	err := RunBackupFDBRestoreVersion(context.Background(), cfg, time.Now(), &out)

	if err == nil || !strings.Contains(err.Error(), "holds no readings") {
		t.Fatalf("an empty record must be refused, got %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("no answer may be printed when none exists: %q", out.String())
	}
}
