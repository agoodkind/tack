// Version-log tests exercise production writes, reads, lookup, and the
// total-loss recovery command against the test SeaweedFS engine.

package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

// recordFDBPoints appends each point through the production writer.
func recordFDBPoints(t *testing.T, store *backupTestStore, points ...fdbRestorablePoint) {
	t.Helper()

	for _, point := range points {
		if err := appendFDBRestorablePoint(
			context.Background(),
			store.versionLogGetter(),
			store.putBytes,
			point,
		); err != nil {
			t.Fatalf("appendFDBRestorablePoint(%d): %v", point.Version, err)
		}
	}
}

// TestFDBVersionLogRoundTripAndLookup checks selection of the newest recorded
// version at or before the requested moment.
func TestFDBVersionLogRoundTripAndLookup(t *testing.T) {
	ctx := context.Background()
	store := newBackupTestStore(t, nil)
	base := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	recordFDBPoints(
		t,
		store,
		fdbRestorablePoint{Version: 100, At: base},
		fdbRestorablePoint{Version: 200, At: base.Add(10 * time.Minute)},
		fdbRestorablePoint{Version: 300, At: base.Add(20 * time.Minute)},
	)

	log, err := readFDBVersionLog(ctx, store.versionLogGetter())
	if err != nil {
		t.Fatalf("readFDBVersionLog: %v", err)
	}

	if len(log.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(log.Points))
	}

	// A later reading includes mutations after the requested restore moment.
	point, found := fdbVersionAt(log, base.Add(15*time.Minute))
	if !found || point.Version != 200 {
		t.Fatalf("version at +15m = %d found=%v, want 200", point.Version, found)
	}

	point, found = fdbVersionAt(log, base.Add(time.Hour))
	if !found || point.Version != 300 {
		t.Fatalf("version at +1h = %d found=%v, want the newest reading", point.Version, found)
	}

	if _, found := fdbVersionAt(log, base.Add(-time.Second)); found {
		t.Fatal("a moment before every reading cannot identify a recorded version")
	}
}

// TestAppendFDBRestorablePointIgnoresARepeatedReading checks deduplication
// without replacing the original observation time while backup remains idle.
func TestAppendFDBRestorablePointIgnoresARepeatedReading(t *testing.T) {
	ctx := context.Background()
	store := newBackupTestStore(t, nil)
	at := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	recordFDBPoints(
		t,
		store,
		fdbRestorablePoint{Version: 100, At: at},
		fdbRestorablePoint{Version: 100, At: at.Add(10 * time.Minute)},
	)

	log, err := readFDBVersionLog(ctx, store.versionLogGetter())
	if err != nil {
		t.Fatalf("readFDBVersionLog: %v", err)
	}

	if len(log.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(log.Points))
	}

	if !log.Points[0].At.Equal(at) {
		t.Fatalf("the original observation time must remain unchanged, got %s", log.Points[0].At)
	}
}

// TestAppendFDBRestorablePointBoundsTheRecord checks maximum-width entries
// beyond the small-object limit. Appends must retain the newest bounded set
// and keep its encoded bytes within the version-log read limit.
func TestAppendFDBRestorablePointBoundsTheRecord(t *testing.T) {
	const appended = 10
	ctx := context.Background()
	store := newBackupTestStore(t, nil)
	seeded := fdbVersionLogMaxEntries - appended/2

	points := make([]fdbRestorablePoint, 0, fdbVersionLogMaxEntries+appended)
	for i := range seeded + appended {
		points = append(points, widestFDBRestorablePoint(i))
	}

	seed, err := json.Marshal(fdbVersionLog{Points: points[:seeded]})
	if err != nil {
		t.Fatalf("marshal the seeded record: %v", err)
	}

	if len(seed) <= smallObjectMaxBytes {
		t.Fatalf(
			"the seeded record is %d bytes and must exceed the %d byte small-object limit",
			len(seed),
			smallObjectMaxBytes,
		)
	}

	store.put(fdbVersionLogKey, seed)

	for _, point := range points[seeded:] {
		if err := appendFDBRestorablePoint(
			ctx,
			store.versionLogGetter(),
			store.putBytes,
			point,
		); err != nil {
			t.Fatalf(
				"appendFDBRestorablePoint(%d) with %d entries recorded: %v",
				point.Version,
				seeded,
				err,
			)
		}
	}

	log, err := readFDBVersionLog(ctx, store.versionLogGetter())
	if err != nil {
		t.Fatalf("readFDBVersionLog: %v", err)
	}

	if len(log.Points) != fdbVersionLogMaxEntries {
		t.Fatalf("points = %d, want %d", len(log.Points), fdbVersionLogMaxEntries)
	}

	dropped := seeded + appended - fdbVersionLogMaxEntries
	if log.Points[0].Version != points[dropped].Version {
		t.Fatalf(
			"oldest retained version = %d, want %d after removing the %d oldest",
			log.Points[0].Version,
			points[dropped].Version,
			dropped,
		)
	}

	if newest := log.Points[len(log.Points)-1]; newest.Version != points[len(points)-1].Version {
		t.Fatalf(
			"newest version = %d, want the last appended %d",
			newest.Version,
			points[len(points)-1].Version,
		)
	}

	stored, err := store.versionLogGetter()(fdbVersionLogKey)
	if err != nil {
		t.Fatalf("read the full record: %v", err)
	}

	if len(stored) > fdbVersionLogMaxBytes {
		t.Fatalf(
			"maximum-width entries encode as %d bytes, exceeding the %d byte read limit",
			len(stored),
			fdbVersionLogMaxBytes,
		)
	}
}

// widestFDBRestorablePoint produces a 20-character version and nanosecond
// timestamp for maximum encoded entry width.
func widestFDBRestorablePoint(i int) fdbRestorablePoint {
	base := time.Date(2026, 9, 18, 4, 0, 0, 999999999, time.UTC)

	return fdbRestorablePoint{
		Version: math.MinInt64 + int64(i),
		At:      base.Add(time.Duration(i) * time.Minute),
	}
}

// TestReadFDBVersionLogAbsent checks empty initial state without a read error.
func TestReadFDBVersionLogAbsent(t *testing.T) {
	log, err := readFDBVersionLog(
		context.Background(),
		newBackupTestStore(t, nil).versionLogGetter(),
	)
	if err != nil {
		t.Fatalf("an absent record is a state, not an error: %v", err)
	}

	if len(log.Points) != 0 {
		t.Fatalf("points = %d, want 0", len(log.Points))
	}
}

// TestRunBackupFDBRestoreVersionAnswersFromTheObjectStore checks the operator
// command without a surviving database cluster.
func TestRunBackupFDBRestoreVersionAnswersFromTheObjectStore(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 18, 4, 0, 0, 0, time.UTC)
	store := newBackupTestStore(t, nil)
	recordFDBPoints(
		t,
		store,
		fdbRestorablePoint{Version: 100720665, At: base},
		fdbRestorablePoint{Version: 100999999, At: base.Add(10 * time.Minute)},
	)
	cfg := store.config()

	var out bytes.Buffer
	if err := RunBackupFDBRestoreVersion(ctx, cfg, base.Add(5*time.Minute), &out); err != nil {
		t.Fatalf("RunBackupFDBRestoreVersion: %v", err)
	}

	if !strings.Contains(out.String(), "restore to version 100720665") {
		t.Fatalf("output must identify the version before the target:\n%s", out.String())
	}

	if !strings.Contains(out.String(), "2026-09-18T04:00:00Z") {
		t.Fatalf("output must date the selected version:\n%s", out.String())
	}

	out.Reset()
	err := RunBackupFDBRestoreVersion(ctx, cfg, base.Add(-time.Hour), &out)
	if err == nil || !strings.Contains(err.Error(), "oldest recorded reading") {
		t.Fatalf("a moment before the record must fail with its bound, got %v", err)
	}
}

// TestRunBackupFDBRestoreVersionRefusesAnEmptyRecord checks rejection without
// printing a version when the store does not contain any readings.
func TestRunBackupFDBRestoreVersionRefusesAnEmptyRecord(t *testing.T) {
	cfg := newBackupTestStore(t, nil).config()
	var out bytes.Buffer

	err := RunBackupFDBRestoreVersion(context.Background(), cfg, time.Now(), &out)

	if err == nil || !strings.Contains(err.Error(), "does not contain readings") {
		t.Fatalf("an empty record must fail lookup, got %v", err)
	}

	if out.Len() != 0 {
		t.Fatalf("an empty record must not produce a version: %q", out.String())
	}
}
