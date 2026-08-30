package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

const drillDestURL = "blobstore://drill-access:drill-secret@fdb-blobstore-host:8333/20260830T000000Z" + // gitleaks:allow test placeholder
	"?bucket=tack-backups&region=us-east-1&secure_connection=0"

// describeWithTimestamps is the shape `fdbbackup describe --version-timestamps`
// prints on foundationdb 7.4.6, per the format strings compiled into its
// fdbbackup binary.
const describeWithTimestamps = "URL: " + drillDestURL + "\n" +
	"Restorable: true\n" +
	"Partitioned logs: false\n" +
	"SnapshotBytes: 1048576\n" +
	"MinLogBeginVersion:      100000000 (2026/08/30.00:00:00+0000)\n" +
	"ContiguousLogEndVersion: 100731044 (2026/08/30.01:10:03+0000)\n" +
	"MaxLogEndVersion:        100731044 (2026/08/30.01:10:03+0000)\n" +
	"MinRestorableVersion:    100700000 (2026/08/30.01:00:00+0000)\n" +
	"MaxRestorableVersion:    100731044 (2026/08/30.01:10:03+0000)\n"

// TestFDBRestoreShellCommandRestoresLatestByDefault locks the unchanged
// default: with no target time the drill runs exactly the command it ran
// before point-in-time restore existed, naming neither a moment nor the source
// cluster.
func TestFDBRestoreShellCommandRestoresLatestByDefault(t *testing.T) {
	got := fdbRestoreShellCommand(drillDestURL, time.Time{}, 1800)

	want := "timeout 1800 fdbrestore start --dest-cluster-file /var/fdb/fdb.cluster" +
		" -r '" + drillDestURL + "' --waitfordone"
	if got != want {
		t.Fatalf("default restore command changed:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "--timestamp") || strings.Contains(got, "--orig-cluster-file") {
		t.Fatalf("default restore must name no target time and no source cluster: %q", got)
	}
}

// TestFDBRestoreShellCommandCarriesTargetAndSourceCluster proves a target time
// reaches fdbrestore together with the source cluster file it needs to convert
// that time to a version, and that the restore still writes to the throwaway.
func TestFDBRestoreShellCommandCarriesTargetAndSourceCluster(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)

	got := fdbRestoreShellCommand(drillDestURL, target, 1800)

	for _, want := range []string{
		"--dest-cluster-file /var/fdb/fdb.cluster",
		"--timestamp '2026/08/30.01:05:00+0000'",
		"--orig-cluster-file /tack-orig-fdb/fdb.cluster",
		"--waitfordone",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("restore command missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "--dest-cluster-file "+fdbOrigClusterFilePath) {
		t.Fatalf("restore must never write to the source cluster: %q", got)
	}
	// FoundationDB falls back to /etc/foundationdb/fdb.cluster when no cluster
	// file is named, so the live cluster must not sit on that path inside the
	// throwaway container.
	if strings.Contains(fdbOrigClusterFilePath, "/etc/foundationdb/") {
		t.Fatalf("source cluster file must not occupy FoundationDB's default path: %q", fdbOrigClusterFilePath)
	}
}

// TestFDBTargetTimeMountIsReadOnlySourceCluster proves the bind that carries
// the live cluster file into the throwaway is read-only and lands on the path
// the restore names.
func TestFDBTargetTimeMountIsReadOnlySourceCluster(t *testing.T) {
	if !strings.HasSuffix(fdbOrigClusterMount, ":"+fdbOrigClusterFilePath+":ro") {
		t.Fatalf("source cluster mount must be read-only at the named path: %q", fdbOrigClusterMount)
	}
}

// TestFDBDescribeShellCommandReadsWindowFromSourceCluster proves the window
// lookup asks the source cluster and requests wall-clock timestamps, without
// which the window cannot be compared to an operator's target time.
func TestFDBDescribeShellCommandReadsWindowFromSourceCluster(t *testing.T) {
	got := fdbDescribeShellCommand(drillDestURL, 1800)

	for _, want := range []string{
		"fdbbackup describe",
		"-d '" + drillDestURL + "'",
		"-C " + fdbOrigClusterFilePath,
		"--version-timestamps",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("describe command missing %q: %q", want, got)
		}
	}
}

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

// TestFDBRestorableWindowRejectsUnusableDescribe covers every describe that
// cannot establish a window. Each must fail: a window nobody could read is
// exactly when a restore would otherwise fall back to the latest data and call
// it success.
func TestFDBRestorableWindowRejectsUnusableDescribe(t *testing.T) {
	tests := []struct {
		name     string
		describe string
		want     string
	}{
		{
			name: "no version timestamps",
			describe: "Restorable: true\n" +
				"MinRestorableVersion:    100700000 (maxLogEnd -0.01 days)\n" +
				"MaxRestorableVersion:    100731044 (maxLogEnd -0.00 days)\n",
			want: "--version-timestamps",
		},
		{
			name:     "not restorable",
			describe: "URL: " + drillDestURL + "\nRestorable: false\nSnapshotBytes: 0\n",
			want:     "not report the backup as restorable",
		},
		{
			// The destination URL is echoed above the verdict, so a backup
			// name that happens to spell the verdict must not stand in for it.
			name: "verdict spelled inside the destination URL",
			describe: "URL: blobstore://k:s@host:8333/Restorable: true?bucket=tack-backups\n" + // gitleaks:allow test placeholder
				"Restorable: false\nSnapshotBytes: 0\n",
			want: "not report the backup as restorable",
		},
		{
			name: "window end missing",
			describe: "Restorable: true\n" +
				"MinRestorableVersion:    100700000 (2026/08/30.01:00:00+0000)\n",
			want: "has no MaxRestorableVersion: line",
		},
		{
			name: "inverted window",
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
				t.Fatal("expected an unusable describe to fail")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error must explain the failure, want %q in: %v", test.want, err)
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
			t.Fatalf("target %s inside the window was refused: %v", formatFDBTime(target), err)
		}
	}
}

// TestAssertTargetWithinWindowRefusesOutsideAndNamesTheWindow is the guard that
// keeps an unreachable moment from silently becoming "the latest".
func TestAssertTargetWithinWindowRefusesOutsideAndNamesTheWindow(t *testing.T) {
	window := fdbRestorableWindow{
		Min: time.Date(2026, 8, 30, 1, 0, 0, 0, time.UTC),
		Max: time.Date(2026, 8, 30, 1, 10, 3, 0, time.UTC),
	}
	tests := map[string]time.Time{
		"before the window": time.Date(2026, 8, 29, 23, 0, 0, 0, time.UTC),
		"after the window":  time.Date(2026, 8, 30, 2, 0, 0, 0, time.UTC),
	}

	for name, target := range tests {
		t.Run(name, func(t *testing.T) {
			err := assertTargetWithinWindow(target, window)
			if err == nil {
				t.Fatal("a target outside the restorable window must be refused")
			}
			for _, want := range []string{
				formatFDBTime(target),
				formatFDBTime(window.Min),
				formatFDBTime(window.Max),
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("refusal must name %q so the operator can pick a reachable moment: %v", want, err)
				}
			}
		})
	}
}

// TestParseFDBTargetTime proves every accepted form lands on the same moment,
// including one written in a non-UTC offset.
func TestParseFDBTargetTime(t *testing.T) {
	want := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	tests := []struct {
		name  string
		input string
	}{
		{name: "rfc3339 utc", input: "2026-08-30T01:05:00Z"},
		{name: "rfc3339 offset", input: "2026-08-29T21:05:00-04:00"},
		{name: "foundationdb form", input: "2026/08/30.01:05:00+0000"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseFDBTargetTime(test.input)
			if err != nil {
				t.Fatalf("parseFDBTargetTime(%q): %v", test.input, err)
			}
			if !got.Equal(want) {
				t.Fatalf("parseFDBTargetTime(%q) = %s, want %s",
					test.input, formatFDBTime(got), formatFDBTime(want))
			}
		})
	}
}

// TestParseFDBTargetTimeRefusesAmbiguousInput keeps a zone-less or unparseable
// target from reaching a restore, where it could only mean the wrong moment.
func TestParseFDBTargetTimeRefusesAmbiguousInput(t *testing.T) {
	for _, input := range []string{"yesterday", "2026-08-30", "2026-08-30T01:05:00", ""} {
		if _, err := parseFDBTargetTime(input); err == nil {
			t.Fatalf("parseFDBTargetTime(%q) must fail rather than guess a moment", input)
		}
	}
}

// TestAssertFDBTargetRestorableSkipsTheWindowCheckByDefault proves the default
// drill never reads the window and never reaches the source cluster: the
// context carries no Docker client, so any attempt to exec would panic.
func TestAssertFDBTargetRestorableSkipsTheWindowCheckByDefault(t *testing.T) {
	drill := &restoreDrillCtx{
		Cfg:           &config.Config{BackupFDBTimeoutSeconds: 1800},
		Cli:           nil,
		RunID:         "run",
		YBPass:        "pass",
		YBRunKey:      "",
		FDBTargetTime: time.Time{},
	}

	if err := assertFDBTargetRestorable(context.Background(), drill, "tack-rtfdb-run", drillDestURL); err != nil {
		t.Fatalf("a drill with no target time must not check a window: %v", err)
	}
}
