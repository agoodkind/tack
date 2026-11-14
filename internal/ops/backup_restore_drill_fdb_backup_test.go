package ops

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// describeWindow reproduces FoundationDB 7.4.6 describe output for the
// production window parser.
func describeWindow(minStamp, maxStamp string) string {
	return "URL: " + drillDestURL + "\n" +
		"Restorable: true\n" +
		"Partitioned logs: false\n" +
		"SnapshotBytes: 1048576\n" +
		"MinRestorableVersion:    100700000 (" + minStamp + ")\n" +
		"MaxRestorableVersion:    100731044 (" + maxStamp + ")\n"
}

// storeDescribes supplies engine output or errors per session and records
// description requests and skips for selection assertions.
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
// selection from two continuous-backup sessions when only the older window
// includes the requested time.
func TestChooseFDBBackupForTargetTakesAnOlderSessionThatCoversTheTarget(t *testing.T) {
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

	name, window, err := chooseFDBBackupForTarget(
		target,
		[]string{"20260829T000000Z", "20260830T000000Z"},
		store.describe,
		store.skip,
	)
	if err != nil {
		t.Fatalf("a target retained by the older session must succeed: %v", err)
	}

	if name != "20260829T000000Z" {
		t.Fatalf("chose %q, want the older session that covers the target", name)
	}

	if !window.Min.Equal(time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("returned window differs from the chosen session: %s", formatFDBTime(window.Min))
	}

	if len(store.skipped) != 1 || !strings.HasPrefix(store.skipped[0], "20260830T000000Z: ") {
		t.Fatalf("the newer session must include a skip reason, got %v", store.skipped)
	}
}

// TestChooseFDBBackupForTargetStopsAtTheNewestCoveringSession checks selection
// of the newest covering session without inspecting an older covering session.
func TestChooseFDBBackupForTargetStopsAtTheNewestCoveringSession(t *testing.T) {
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow(
				"2026/08/29.00:00:00+0000",
				"2026/08/30.02:00:00+0000",
			),
			"20260830T000000Z": describeWindow(
				"2026/08/30.01:00:00+0000",
				"2026/08/30.02:00:00+0000",
			),
		},
		failing: nil,
		asked:   nil,
		skipped: nil,
	}
	target := time.Date(2026, 8, 30, 1, 30, 0, 0, time.UTC)

	name, _, err := chooseFDBBackupForTarget(
		target,
		[]string{"20260829T000000Z", "20260830T000000Z"},
		store.describe,
		store.skip,
	)
	if err != nil {
		t.Fatalf("a target included in both windows must succeed: %v", err)
	}

	if name != "20260830T000000Z" {
		t.Fatalf("chose %q, want the newest covering session", name)
	}

	if len(store.asked) != 1 || store.asked[0] != "20260830T000000Z" {
		t.Fatalf("selection must stop after the newest covering session, read %v", store.asked)
	}
}

// TestChooseFDBBackupForTargetPassesOverASessionItCannotDescribe checks that
// an unreadable window records a reason and permits an older covering backup.
func TestChooseFDBBackupForTargetPassesOverASessionItCannotDescribe(t *testing.T) {
	store := &storeDescribes{
		outputs: map[string]string{
			"20260829T000000Z": describeWindow(
				"2026/08/29.00:00:00+0000",
				"2026/08/30.02:00:00+0000",
			),
		},
		failing: map[string]error{
			"20260830T000000Z": errors.New("fdbbackup describe exited 1: no such backup"),
		},
		asked:   nil,
		skipped: nil,
	}
	target := time.Date(2026, 8, 30, 1, 30, 0, 0, time.UTC)

	name, _, err := chooseFDBBackupForTarget(
		target,
		[]string{"20260829T000000Z", "20260830T000000Z"},
		store.describe,
		store.skip,
	)
	if err != nil {
		t.Fatalf("an older covering session must succeed: %v", err)
	}

	if name != "20260829T000000Z" {
		t.Fatalf("chose %q, want the readable covering session", name)
	}

	if len(store.skipped) != 1 || !strings.Contains(store.skipped[0], "no such backup") {
		t.Fatalf(
			"the unreadable session must include a skip reason, got %v",
			store.skipped,
		)
	}
}

// TestChooseFDBBackupForTargetReportsEveryExcludedSession checks that refusal
// includes the target and every session's window or read failure.
func TestChooseFDBBackupForTargetReportsEveryExcludedSession(t *testing.T) {
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
		failing: map[string]error{
			"20260828T000000Z": errors.New("fdbbackup describe exited 1: expired"),
		},
		asked:   nil,
		skipped: nil,
	}
	target := time.Date(2026, 8, 30, 0, 30, 0, 0, time.UTC)

	name, _, err := chooseFDBBackupForTarget(
		target,
		[]string{
			"20260828T000000Z",
			"20260829T000000Z",
			"20260830T000000Z",
		},
		store.describe,
		store.skip,
	)
	if err == nil {
		t.Fatalf("a target outside every window must fail, chose %q", name)
	}

	for _, want := range []string{
		"2026/08/30.00:30:00+0000",
		"20260830T000000Z: restorable window 2026/08/30.01:00:00+0000 .. 2026/08/30.01:10:03+0000",
		"20260829T000000Z: restorable window 2026/08/29.00:00:00+0000 .. 2026/08/29.23:00:00+0000",
		"20260828T000000Z: fdbbackup describe exited 1: expired",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf(
				"the refusal must include %q for target selection, got: %v",
				want,
				err,
			)
		}
	}
}
