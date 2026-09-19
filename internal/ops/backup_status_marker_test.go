package ops

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

// TestBackupStatusMarkerRoundTrip writes a marker through the production writer
// into the object store and reads it back through the production reader, so
// the two can never disagree on the key or the field names. The stored bytes
// are asserted too, because the marker is an operator-readable artifact.
func TestBackupStatusMarkerRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newBackupTestStore(t, nil)
	at := time.Date(2026, 8, 29, 3, 30, 0, 0, time.UTC)

	if err := writeBackupStatusMarker(ctx, store.putBytes, backupStalenessRehearsalName, at,
		"restore drill rt20260829T033000Z passed: fdb, yugabyte"); err != nil {
		t.Fatalf("writeBackupStatusMarker: %v", err)
	}

	body, ok := store.object("backup-status/rehearsal.json")
	if !ok {
		t.Fatal("marker landed under an unexpected key")
	}
	wantBody := `{"at":"2026-08-29T03:30:00Z","detail":"restore drill rt20260829T033000Z passed: fdb, yugabyte"}`
	if string(body) != wantBody {
		t.Fatalf("marker body mismatch:\n got=%s\nwant=%s", body, wantBody)
	}

	marker, found, err := readBackupStatusMarker(ctx, store.getBytes, backupStalenessRehearsalName)
	if err != nil {
		t.Fatalf("readBackupStatusMarker: %v", err)
	}
	if !found {
		t.Fatal("the marker just written must be found")
	}
	if !marker.At.Equal(at) {
		t.Fatalf("marker at = %s, want %s", marker.At, at)
	}
	if !strings.Contains(marker.Detail, "passed: fdb, yugabyte") {
		t.Fatalf("marker detail = %q", marker.Detail)
	}
}

// TestBackupStatusMarkerWrittenInUTC proves a marker written by a host on a
// non-UTC clock still stores an absolute UTC instant, so ages computed against
// it are comparable across hosts.
func TestBackupStatusMarkerWrittenInUTC(t *testing.T) {
	ctx := context.Background()
	store := newBackupTestStore(t, nil)
	zone := time.FixedZone("PDT", -7*60*60)
	at := time.Date(2026, 8, 29, 3, 30, 0, 0, zone)

	if err := writeBackupStatusMarker(ctx, store.putBytes, backupStalenessReplicationName, at, "0 dead nodes"); err != nil {
		t.Fatalf("writeBackupStatusMarker: %v", err)
	}
	stored, _ := store.object("backup-status/replication.json")
	body := string(stored)
	if !strings.Contains(body, `"at":"2026-08-29T10:30:00Z"`) {
		t.Fatalf("marker must store UTC, got %s", body)
	}

	marker, found, err := readBackupStatusMarker(ctx, store.getBytes, backupStalenessReplicationName)
	if err != nil || !found {
		t.Fatalf("readBackupStatusMarker: %v found=%v", err, found)
	}
	if !marker.At.Equal(at) {
		t.Fatalf("marker at = %s, want the same instant as %s", marker.At, at)
	}
}

// TestReadBackupStatusMarkerAbsent proves a mechanism that never recorded a
// success reads as absent rather than as an error or a zero-time success.
func TestReadBackupStatusMarkerAbsent(t *testing.T) {
	marker, found, err := readBackupStatusMarker(context.Background(), newBackupTestStore(t, nil).getBytes,
		backupStalenessRehearsalName)
	if err != nil {
		t.Fatalf("a missing marker is a state, not an error: %v", err)
	}
	if found {
		t.Fatal("no marker was written, so none may be found")
	}
	if !marker.At.IsZero() {
		t.Fatalf("absent marker carries a timestamp: %s", marker.At)
	}
}

// TestReadBackupStatusMarkerRejectsUndatedMarker proves a marker that decodes
// but carries no timestamp is refused. Accepting it would report a success at
// the zero time, which reads as an enormous age rather than as the corruption
// it is.
func TestReadBackupStatusMarkerRejectsUndatedMarker(t *testing.T) {
	store := newBackupTestStore(t, nil)
	store.put(backupStatusKey(backupStalenessReplicationName), []byte(`{"detail":"no timestamp"}`))

	if _, found, err := readBackupStatusMarker(context.Background(), store.getBytes,
		backupStalenessReplicationName); err == nil || found {
		t.Fatalf("an undated marker must be an error, got found=%v err=%v", found, err)
	}
}

// TestGetObjectBytesRefusesAnOversizedObject proves an object past the
// in-memory limit is refused rather than buffered. The staleness check is what
// reports silence, so a corrupted or hostile marker must not be able to take
// the check itself down; the run instead fails loudly with the size named.
func TestGetObjectBytesRefusesAnOversizedObject(t *testing.T) {
	ctx := context.Background()
	const key = "backup-status/oversized.json"
	oversized := bytes.Repeat([]byte("x"), smallObjectMaxBytes+1)
	atLimit := bytes.Repeat([]byte("y"), smallObjectMaxBytes)

	store := newBackupTestStore(t, map[string][]byte{
		key:               oversized,
		"backup-status/2": atLimit,
	})
	client, cfg := store.client, store.config()

	if _, err := getObjectBytes(ctx, client, cfg.BackupS3BucketMain, key); err == nil {
		t.Fatal("an object past the in-memory limit must be refused")
	} else if !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("the error must name the limit, got: %v", err)
	}

	body, err := getObjectBytes(ctx, client, cfg.BackupS3BucketMain, "backup-status/2")
	if err != nil {
		t.Fatalf("an object exactly at the limit must still read: %v", err)
	}
	if len(body) != smallObjectMaxBytes {
		t.Fatalf("read %d bytes, want %d", len(body), smallObjectMaxBytes)
	}
}

// TestRestoreDrillRehearsalMarkerIsReadBackByTheCheck proves the two halves of
// the rehearsal metric meet: the marker a passing drill records is the marker
// the staleness check dates the rehearsal from, at the same key, carrying the
// drill's run id and the legs it proved.
func TestRestoreDrillRehearsalMarkerIsReadBackByTheCheck(t *testing.T) {
	drilledAt := time.Date(2026, 8, 29, 6, 0, 0, 0, time.UTC)
	nowFunc = func() time.Time { return drilledAt }
	t.Cleanup(func() { nowFunc = time.Now })

	ctx := context.Background()
	store := newBackupTestStore(t, nil)
	if err := recordRestoreDrillRehearsal(ctx, store.putBytes, "rt20260829T060000Z-42",
		[]string{"fdb", "yugabyte"}); err != nil {
		t.Fatalf("recordRestoreDrillRehearsal: %v", err)
	}

	marker, found, err := readBackupStatusMarker(ctx, store.getBytes, backupStalenessRehearsalName)
	if err != nil || !found {
		t.Fatalf("the check must find the drill's marker: found=%v err=%v", found, err)
	}
	if !marker.At.Equal(drilledAt) {
		t.Fatalf("marker at = %s, want the drill's clock %s", marker.At, drilledAt)
	}
	if marker.Detail != "restore drill rt20260829T060000Z-42 passed: fdb, yugabyte" {
		t.Fatalf("marker detail = %q", marker.Detail)
	}

	// The age the report would show comes straight off that marker.
	metric := knownBackupStalenessMetric(ctx, backupStalenessRehearsalName,
		drilledAt.Add(2*time.Hour), marker.At, 8*24*time.Hour, marker.Detail)
	if metric.stale() {
		t.Fatal("a drill two hours old must not read as stale")
	}
}

// TestBackupStatusKey pins the object keys the writers and the check share.
func TestBackupStatusKey(t *testing.T) {
	tests := map[string]string{
		backupStalenessRehearsalName:   "backup-status/rehearsal.json",
		backupStalenessReplicationName: "backup-status/replication.json",
	}
	for metric, want := range tests {
		if got := backupStatusKey(metric); got != want {
			t.Errorf("backupStatusKey(%q) = %q, want %q", metric, got, want)
		}
	}
}
