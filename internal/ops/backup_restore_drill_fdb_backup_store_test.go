package ops

import (
	"slices"
	"testing"
	"time"
)

// TestFDBBackupSelectionReadsEverySessionTheStoreHolds runs the selection from
// the object store in, the way the drill does: the backup names come out of a
// real S3 listing of the backups/ markers, through the real client and the fake
// store the drill tests share, and the target is then checked against each
// session's window newest first. The store below holds two sessions because
// the continuous backup was started twice, with the engine's data folders and
// the backups/ placeholder beside the markers; the target is a moment only the
// older session retains, and it must be restored from that session.
func TestFDBBackupSelectionReadsEverySessionTheStoreHolds(t *testing.T) {
	client, cfg := newFakeBackupObjectStore(t, "tack-backups", map[string][]byte{
		"backups/":                        {},
		"backups/20260829T000000Z":        {},
		"backups/20260830T000000Z":        {},
		"20260829T000000Z/data/0/range-1": []byte("range"),
		"20260830T000000Z/data/0/range-1": []byte("range"),
		"20260830T000000Z/logs/0/log-1":   []byte("log"),
	})
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow("2026/08/29.00:00:00+0000", "2026/08/29.23:00:00+0000"),
			"20260830T000000Z": describeWindow("2026/08/30.01:00:00+0000", "2026/08/30.01:10:03+0000"),
		},
		failing: nil, asked: nil, skipped: nil,
	}
	target := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	markers, err := listImmediateObjects(t.Context(), client, cfg.BackupS3BucketMain, fdbBackupMarkerPrefix)
	if err != nil {
		t.Fatalf("list the backups/ markers: %v", err)
	}
	names := fdbBackupNames(markers)
	name, _, err := chooseFDBBackupForTarget(target, names, store.describe, store.skip)

	if want := []string{"20260829T000000Z", "20260830T000000Z"}; !slices.Equal(names, want) {
		t.Fatalf("the store's markers named %v, want the two sessions oldest first %v", names, want)
	}
	if err != nil {
		t.Fatalf("a target the older session retains must not be refused: %v", err)
	}
	if name != "20260829T000000Z" {
		t.Fatalf("chose %q, want the older session that covers the target", name)
	}
	if want := []string{"20260830T000000Z", "20260829T000000Z"}; !slices.Equal(store.asked, want) {
		t.Fatalf("the sessions must be described newest first, described %v", store.asked)
	}
}
