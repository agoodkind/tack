package ops

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// describeWindow renders the output format of
// fdbbackup describe --version-timestamps in FoundationDB 7.4.6. Each session's
// window uses the real parser instead of a preconstructed selection input.
func describeWindow(minStamp, maxStamp string) string {
	return "URL: " + drillDestURL + "\n" +
		"Restorable: true\n" +
		"Partitioned logs: false\n" +
		"SnapshotBytes: 1048576\n" +
		"MinRestorableVersion:    100700000 (" + minStamp + ")\n" +
		"MaxRestorableVersion:    100731044 (" + maxStamp + ")\n"
}

// storeDescribes substitutes for the describe exec over several backup
// sessions. Each name selects engine output or an exec error. The helper
// records requested sessions for the test to inspect.
type storeDescribes struct {
	outputs map[string]string
	failing map[string]error
	asked   []string
	skipped []string
}

func (s *storeDescribes) describe(name string) (fdbRestorableWindow, error) {
	s.asked = append(s.asked, name)
	if err, failing := s.failing[name]; failing {
		return fdbRestorableWindow{}, err
	}
	return fdbRestorableWindowFromDescribe(s.outputs[name])
}

func (s *storeDescribes) skip(name, reason string) {
	s.skipped = append(s.skipped, name+": "+reason)
}

// TestChooseFDBBackupForTargetTakesAnOlderSessionThatCoversTheTarget checks
// the earlier defect in which selection consulted only the newest backup and
// rejected a target that an older session retained.
//
// The two sessions represent two starts of continuous backup. The target
// precedes the newer window and falls within the older window. Selection must
// choose the older session.
func TestChooseFDBBackupForTargetTakesAnOlderSessionThatCoversTheTarget(t *testing.T) {
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow("2026/08/29.00:00:00+0000", "2026/08/29.23:00:00+0000"),
			"20260830T000000Z": describeWindow("2026/08/30.01:00:00+0000", "2026/08/30.01:10:03+0000"),
		},
		failing: nil, asked: nil, skipped: nil,
	}
	target := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)

	name, window, err := chooseFDBBackupForTarget(target,
		[]string{"20260829T000000Z", "20260830T000000Z"}, store.describe, store.skip)
	if err != nil {
		t.Fatalf("a target the older session retains must not be refused: %v", err)
	}
	if name != "20260829T000000Z" {
		t.Fatalf("chose %q, want the older session that covers the target", name)
	}
	if !window.Min.Equal(time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("the window returned is not the chosen session's: %s", formatFDBTime(window.Min))
	}
	if len(store.skipped) != 1 || !strings.HasPrefix(store.skipped[0], "20260830T000000Z: ") {
		t.Fatalf("the newer session must be passed over with its reason, got %v", store.skipped)
	}
}

// TestChooseFDBBackupForTargetStopsAtTheNewestCoveringSession checks that
// selection chooses the newest session when several sessions cover the target.
// The search stops without reading the older session's window.
func TestChooseFDBBackupForTargetStopsAtTheNewestCoveringSession(t *testing.T) {
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow("2026/08/29.00:00:00+0000", "2026/08/30.02:00:00+0000"),
			"20260830T000000Z": describeWindow("2026/08/30.01:00:00+0000", "2026/08/30.02:00:00+0000"),
		},
		failing: nil, asked: nil, skipped: nil,
	}
	target := time.Date(2026, 8, 30, 1, 30, 0, 0, time.UTC)

	name, _, err := chooseFDBBackupForTarget(target,
		[]string{"20260829T000000Z", "20260830T000000Z"}, store.describe, store.skip)
	if err != nil {
		t.Fatalf("a target both sessions cover must not be refused: %v", err)
	}
	if name != "20260830T000000Z" {
		t.Fatalf("chose %q, want the newest of the sessions that cover the target", name)
	}
	if len(store.asked) != 1 || store.asked[0] != "20260830T000000Z" {
		t.Fatalf("the search must stop at the newest covering session, described %v", store.asked)
	}
}

// TestChooseFDBBackupForTargetPassesOverASessionItCannotDescribe checks that
// an unreadable window does not end the search for an older covering session.
// Selection preserves the reason for skipping the unreadable session.
func TestChooseFDBBackupForTargetPassesOverASessionItCannotDescribe(t *testing.T) {
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow("2026/08/29.00:00:00+0000", "2026/08/30.02:00:00+0000"),
		},
		failing: map[string]error{"20260830T000000Z": errors.New("fdbbackup describe exited 1: no such backup")},
		asked:   nil,
		skipped: nil,
	}
	target := time.Date(2026, 8, 30, 1, 30, 0, 0, time.UTC)

	name, _, err := chooseFDBBackupForTarget(target,
		[]string{"20260829T000000Z", "20260830T000000Z"}, store.describe, store.skip)
	if err != nil {
		t.Fatalf("an older session that covers the target must be taken: %v", err)
	}
	if name != "20260829T000000Z" {
		t.Fatalf("chose %q, want the session that could be described and covers the target", name)
	}
	if len(store.skipped) != 1 || !strings.Contains(store.skipped[0], "no such backup") {
		t.Fatalf("the unreadable session must be passed over with its reason, got %v", store.skipped)
	}
}

// TestChooseFDBBackupForTargetNamesEverySessionWhenNoneCovers checks the
// refusal when every session excludes the target. The error identifies each
// session with its window or read failure. The operator can use those details
// to select a restorable time.
func TestChooseFDBBackupForTargetNamesEverySessionWhenNoneCovers(t *testing.T) {
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow("2026/08/29.00:00:00+0000", "2026/08/29.23:00:00+0000"),
			"20260830T000000Z": describeWindow("2026/08/30.01:00:00+0000", "2026/08/30.01:10:03+0000"),
		},
		failing: map[string]error{"20260828T000000Z": errors.New("fdbbackup describe exited 1: expired")},
		asked:   nil,
		skipped: nil,
	}
	target := time.Date(2026, 8, 30, 0, 30, 0, 0, time.UTC)

	name, _, err := chooseFDBBackupForTarget(target,
		[]string{"20260828T000000Z", "20260829T000000Z", "20260830T000000Z"}, store.describe, store.skip)

	if err == nil {
		t.Fatalf("a target no session covers must be refused, chose %q", name)
	}
	for _, want := range []string{
		"2026/08/30.00:30:00+0000",
		"20260830T000000Z: restorable window 2026/08/30.01:00:00+0000 .. 2026/08/30.01:10:03+0000",
		"20260829T000000Z: restorable window 2026/08/29.00:00:00+0000 .. 2026/08/29.23:00:00+0000",
		"20260828T000000Z: fdbbackup describe exited 1: expired",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal must carry %q so the operator can pick a reachable moment, got: %v", want, err)
		}
	}
}
