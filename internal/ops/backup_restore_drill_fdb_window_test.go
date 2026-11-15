package ops

import (
	"strings"
	"testing"
	"time"
)

// describeWithTimestamps matches FoundationDB 7.4.6 describe output with
// --version-timestamps, including the earliest and latest restorable times.
const describeWithTimestamps = "URL: " + drillDestURL + "\n" +
	"Restorable: true\n" +
	"Partitioned logs: false\n" +
	"SnapshotBytes: 1048576\n" +
	"MinLogBeginVersion:      100000000 (2026/08/30.00:00:00+0000)\n" +
	"ContiguousLogEndVersion: 100731044 (2026/08/30.01:10:03+0000)\n" +
	"MaxLogEndVersion:        100731044 (2026/08/30.01:10:03+0000)\n" +
	"MinRestorableVersion:    100700000 (2026/08/30.01:00:00+0000)\n" +
	"MaxRestorableVersion:    100731044 (2026/08/30.01:10:03+0000)\n"

func TestFDBRestorableWindowFromDescribe(t *testing.T) {
	window, err := fdbRestorableWindowFromDescribe(describeWithTimestamps)
	if err != nil {
		t.Fatalf("fdbRestorableWindowFromDescribe: %v", err)
	}

	wantMin := time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC)
	wantMax := time.Date(2026, 8, 30, 1, 10, 3, 0, time.UTC)
	if !window.Min.Equal(wantMin) || !window.Max.Equal(wantMax) {
		t.Fatalf("window = %s .. %s, want %s .. %s",
			formatFDBTime(window.Min), formatFDBTime(window.Max),
			formatFDBTime(wantMin), formatFDBTime(wantMax))
	}
}

// TestFDBRestorableWindowRejectsUnusableDescribe checks refusal of missing or
// invalid bounds. A failed lookup must not substitute a restore of the latest data.
func TestFDBRestorableWindowRejectsUnusableDescribe(t *testing.T) {
	tests := []struct {
		name     string
		describe string
		want     string
	}{
		{
			name: "parser rejects relative times",
			describe: "Restorable: true\n" +
				"MinRestorableVersion:    100700000 (maxLogEnd -0.01 days)\n" +
				"MaxRestorableVersion:    100731044 (maxLogEnd -0.00 days)\n",
			want: "--version-timestamps",
		},
		{
			name:     "parser rejects a nonrestorable backup",
			describe: "URL: " + drillDestURL + "\nRestorable: false\nSnapshotBytes: 0\n",
			want:     "not report the backup as restorable",
		},
		{
			// A matching phrase in the URL cannot replace the complete verdict line.
			name: "parser rejects a verdict embedded in the URL",
			describe: "URL: blobstore://k:s@host:8333/Restorable: true?bucket=tack-backups\n" + // gitleaks:allow test placeholder
				"Restorable: false\nSnapshotBytes: 0\n",
			want: "not report the backup as restorable",
		},
		{
			name: "parser rejects a missing window end",
			describe: "Restorable: true\n" +
				"MinRestorableVersion:    100700000 (2026/08/30.01:00:00+0000)\n",
			want: "does not include a MaxRestorableVersion: line",
		},
		{
			name: "parser rejects inverted bounds",
			describe: "Restorable: true\n" +
				"MinRestorableVersion:    100731044 (2026/08/30.01:10:03+0000)\n" +
				"MaxRestorableVersion:    100700000 (2026/08/30.01:00:00+0000)\n",
			want: "inverted restorable window",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := fdbRestorableWindowFromDescribe(test.describe)
			if err == nil {
				t.Fatal("parser accepted invalid describe output")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error must include %q: %v", test.want, err)
			}
		})
	}
}

func TestAssertTargetWithinWindowAcceptsTheWindowAndItsEdges(t *testing.T) {
	window := fdbRestorableWindow{
		Min: time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC),
		Max: time.Date(2026, 8, 30, 1, 10, 3, 0, time.UTC),
	}
	for _, target := range []time.Time{window.Min, window.Max, window.Min.Add(time.Minute)} {
		if err := assertTargetWithinWindow(target, window); err != nil {
			t.Fatalf("target %s inside the window failed validation: %v", formatFDBTime(target), err)
		}
	}
}

// TestAssertTargetWithinWindowRejectsOutsideAndReportsBounds checks that an
// outside target fails with the requested time and both available bounds.
func TestAssertTargetWithinWindowRejectsOutsideAndReportsBounds(t *testing.T) {
	window := fdbRestorableWindow{
		Min: time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC),
		Max: time.Date(2026, 8, 30, 1, 10, 3, 0, time.UTC),
	}
	tests := map[string]time.Time{
		"validation rejects a target before the window": time.Date(2026, 8, 29, 23, 0, 0, 0, time.UTC),
		"validation rejects a target after the window":  time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC),
	}
	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			err := assertTargetWithinWindow(target, window)
			if err == nil {
				t.Fatal("validation accepted a target outside the restorable window")
			}
			for _, want := range []string{formatFDBTime(target), formatFDBTime(window.Min), formatFDBTime(window.Max)} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error must include time %q: %v", want, err)
				}
			}
		})
	}
}
