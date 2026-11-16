package ops

import (
	"slices"
	"testing"
	"time"
)

// TestFDBBackupSelectionUsesOlderCoveringSession reads real SeaweedFS markers
// through the production S3 client. Two sessions and engine data coexist with
// the backups/ placeholder. Selection must inspect windows newest first and
// choose the older session for a target outside the newer session's window.
func TestFDBBackupSelectionUsesOlderCoveringSession(t *testing.T) {
	objectStore := newBackupTestStore(
		t,
		map[string][]byte{
			"backups/":                        {},
			"backups/20260829T000000Z":        {},
			"backups/20260830T000000Z":        {},
			"20260829T000000Z/data/0/range-1": []byte("range"),
			"20260830T000000Z/data/0/range-1": []byte("range"),
			"20260830T000000Z/logs/0/log-1":   []byte("log"),
		},
	)
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow(
				"2026/08/29.00:00:00+0000",
				"2026/08/29.23:00:00+0000",
			),
			"20260830T000000Z": describeWindow(
				"2026/08/30.01:00:00+0000",
				"2026/08/30.01:10:03+0000",
			),
		},
		failing: nil,
		asked:   nil,
		skipped: nil,
	}
	target := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	markers, err := listImmediateObjects(
		t.Context(),
		objectStore.client,
		objectStore.bucket.Bucket,
		fdbBackupMarkerPrefix,
	)
	if err != nil {
		t.Fatalf("list the backups/ markers: %v", err)
	}

	names := fdbBackupNames(markers)
	name, _, err := chooseFDBBackupForTarget(target, names, store.describe, store.skip)

	if want := []string{"20260829T000000Z", "20260830T000000Z"}; !slices.Equal(names, want) {
		t.Fatalf("marker sessions = %v, want oldest-first sessions %v", names, want)
	}

	if err != nil {
		t.Fatalf("a target retained by the older session must succeed: %v", err)
	}

	if name != "20260829T000000Z" {
		t.Fatalf("chose %q, want the older session that covers the target", name)
	}

	if want := []string{"20260830T000000Z", "20260829T000000Z"}; !slices.Equal(store.asked, want) {
		t.Fatalf("session reads = %v, want newest-first order %v", store.asked, want)
	}
}
