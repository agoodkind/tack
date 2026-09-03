package ops

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

const drillDestURL = "blobstore://drill-access:drill-secret@fdb-blobstore-host:8333/20260830T000000Z" + // gitleaks:allow test placeholder
	"?bucket=tack-backups&region=us-east-1&secure_connection=0"

// hostileDestURL is a destination URL whose backup name carries shell syntax.
// The backup name is read out of the bucket rather than written here, so it is
// only as trustworthy as the objects under backups/, and the URL as a whole
// carries the blobstore access key and secret.
const hostileDestURL = "blobstore://drill-access:drill-secret@fdb-blobstore-host:8333/" + // gitleaks:allow test placeholder
	"20260830T000000Z'; touch /tmp/pwned; '?bucket=tack-backups"

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

// TestFDBRestoreCommandRestoresLatestByDefault locks the unchanged default:
// with no target the drill runs exactly the restore it ran before
// point-in-time restore existed, naming neither a moment nor the source
// cluster.
func TestFDBRestoreCommandRestoresLatestByDefault(t *testing.T) {
	got, err := fdbRestoreCommand(drillDestURL, nil)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with no target: %v", err)
	}

	want := []string{
		"fdbrestore", "start",
		"--dest-cluster-file", "/var/fdb/fdb.cluster",
		"-r", drillDestURL,
		"--waitfordone",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("default restore command changed:\n got %q\nwant %q", got, want)
	}
}

// TestFDBRestoreCommandBoundsNoTotalRunTime is the other half of the drill's
// stall watching. A restore's duration scales with the dataset, so a total-work
// budget would eventually fail a healthy restore for being large. The engine
// vector must therefore carry no such budget, and inactivity must be the only
// thing that ends a restore early.
func TestFDBRestoreCommandBoundsNoTotalRunTime(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	for name, targetTime := range map[string]*time.Time{"latest": nil, "targeted": &target} {
		t.Run(name, func(t *testing.T) {
			got, err := fdbRestoreCommand(drillDestURL, targetTime)
			if err != nil {
				t.Fatalf("fdbRestoreCommand: %v", err)
			}
			if slices.Contains(got, "timeout") {
				t.Fatalf("the restore must not carry a total-work budget: %q", got)
			}
		})
	}
}

// TestFDBRestoreCommandCarriesTargetAndSourceCluster proves a target reaches
// fdbrestore together with the source cluster file it needs to convert that
// time to a version, and that the restore still writes to the throwaway.
func TestFDBRestoreCommandCarriesTargetAndSourceCluster(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)

	got, err := fdbRestoreCommand(drillDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with a whole-second target: %v", err)
	}

	want := []string{
		"fdbrestore", "start",
		"--dest-cluster-file", "/var/fdb/fdb.cluster",
		"-r", drillDestURL,
		"--waitfordone",
		"--timestamp", "2026/08/30.01:05:00+0000",
		"--orig-cluster-file", "/tack-orig-fdb/fdb.cluster",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("targeted restore command changed:\n got %q\nwant %q", got, want)
	}
	// FoundationDB falls back to /etc/foundationdb/fdb.cluster when no cluster
	// file is named, so the live cluster must not sit on that path inside the
	// throwaway container.
	if strings.Contains(fdbOrigClusterFilePath, "/etc/foundationdb/") {
		t.Fatalf("source cluster file must not occupy FoundationDB's default path: %q", fdbOrigClusterFilePath)
	}
}

// TestFDBCommandsCarryTheDestinationAsOneArgument is the guard against a
// destination URL becoming shell syntax. The URL carries the blobstore
// credentials and a backup name read out of the bucket, so quoting it into a
// shell string is what would let a quote in either append commands to a
// container that can reach the live cluster. A vector cannot be escaped from.
//
// Both vectors must be free of shell program text of their own. The describe is
// execed as it stands. The restore reaches a shell, because backgrounding it is
// what lets the drill watch it, but only as that shell's positional parameters:
// a vector that assembled its own `sh -c` would put the destination back into
// program text, which is the defect this guards.
func TestFDBCommandsCarryTheDestinationAsOneArgument(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	restore, err := fdbRestoreCommand(hostileDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand: %v", err)
	}
	describe := fdbDescribeCommand(hostileDestURL)

	for name, command := range map[string][]string{"restore": restore, "describe": describe} {
		t.Run(name, func(t *testing.T) {
			if slices.Contains(command, "sh") || slices.Contains(command, "-c") {
				t.Fatalf("engine vectors must carry no shell of their own: %q", command)
			}
			whole := 0
			for _, arg := range command {
				if arg == hostileDestURL {
					whole++
					continue
				}
				if strings.Contains(arg, "touch /tmp/pwned") {
					t.Fatalf("destination URL leaked out of its own argument: %q", command)
				}
			}
			if whole != 1 {
				t.Fatalf("destination URL must be exactly one whole argument, appeared %d times: %q",
					whole, command)
			}
		})
	}
}

// TestFDBRestoreCommandRefusesSubSecondTarget proves no caller can assemble a
// restore that silently drops the fraction of a second an operator named. The
// flag path refuses it earlier, but a target only becomes a moment here.
func TestFDBRestoreCommandRefusesSubSecondTarget(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 900000000, time.UTC)

	got, err := fdbRestoreCommand(drillDestURL, &target)

	if err == nil {
		t.Fatalf("a sub-second target must not assemble a restore: %q", got)
	}
	if got != nil {
		t.Fatalf("a refused target must yield no command: %q", got)
	}
	for _, want := range []string{"2026-08-30T01:05:00.9Z", fdbTimestampArgForm, "2026/08/30.01:05:00+0000"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name %q so the operator can pick a moment: %v", want, err)
		}
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

// TestFDBDescribeCommandReadsWindowFromSourceCluster proves the window lookup
// asks the source cluster and requests wall-clock timestamps, without which the
// window cannot be compared to an operator's target time. It also keeps the
// bound on its own run time: nothing watches this exec, so a source that
// accepts the connection and then says nothing must end it rather than hang the
// drill. Reading metadata does not grow with the dataset, which is why a bound
// belongs here and not on the restore.
func TestFDBDescribeCommandReadsWindowFromSourceCluster(t *testing.T) {
	got := fdbDescribeCommand(drillDestURL)

	want := []string{
		"timeout", strconv.Itoa(fdbDescribeTimeoutSeconds),
		"fdbbackup", "describe",
		"-d", drillDestURL,
		"-C", fdbOrigClusterFilePath,
		"--version-timestamps",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("describe command changed:\n got %q\nwant %q", got, want)
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

// TestParseFDBTargetTimeRefusesSubSecondInput covers the fraction of a second
// FoundationDB's --timestamp cannot express. Go's time.Parse takes a fraction
// after the seconds even though neither accepted layout declares one, so both
// input forms can carry a precision the restore would drop, landing the drill
// on the whole second before the moment asked for.
func TestParseFDBTargetTimeRefusesSubSecondInput(t *testing.T) {
	for _, input := range []string{
		"2026-08-30T01:05:00.9Z",
		"2026-08-29T21:05:00.000000001-04:00",
		"2026/08/30.01:05:00.9+0000",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := parseFDBTargetTime(input)
			if err == nil {
				t.Fatalf("parseFDBTargetTime(%q) = %s, must refuse a precision the restore drops",
					input, formatFDBTime(got))
			}
			if !strings.Contains(err.Error(), fdbTimestampArgForm) {
				t.Fatalf("refusal must name the form a target has to fit: %v", err)
			}
		})
	}
}

// TestParseFDBTargetTimeKeepsTheZeroTimeExplicit is the other half of that
// guard. The zero instant parses like any other moment, and the drill must
// still treat it as a moment the operator named rather than as no moment at
// all, which is what carrying it in a pointer buys.
func TestParseFDBTargetTimeKeepsTheZeroTimeExplicit(t *testing.T) {
	parsed, err := parseFDBTargetTime("0001-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("parseFDBTargetTime of the zero instant: %v", err)
	}

	latest, err := fdbRestoreCommand(drillDestURL, nil)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with no target: %v", err)
	}
	explicit, err := fdbRestoreCommand(drillDestURL, &parsed)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with the zero instant: %v", err)
	}

	if slices.Equal(explicit, latest) {
		t.Fatalf("an explicit target assembled the latest-point restore: %q", explicit)
	}
	if !slices.Contains(explicit, "0001/01/01.00:00:00+0000") {
		t.Fatalf("an explicit target must reach fdbrestore as itself: %q", explicit)
	}
}

// TestSelectFDBBackupSkipsTheWindowCheckByDefault proves the default drill
// never reads a window and never reaches the source cluster: the context
// carries no Docker client, so any attempt to exec would panic. It restores
// the newest backup, as it did before point-in-time restore existed.
func TestSelectFDBBackupSkipsTheWindowCheckByDefault(t *testing.T) {
	drill := &restoreDrillCtx{
		Cfg:           &config.Config{},
		Cli:           nil,
		RunID:         "run",
		YBPass:        "pass",
		YBRunKey:      "",
		FDBTargetTime: nil,
	}

	name, err := selectFDBBackup(context.Background(), drill, "tack-rtfdb-run",
		[]string{"backups/20260829T000000Z", "backups/20260830T000000Z"})
	if err != nil {
		t.Fatalf("a drill with no target time must not check a window: %v", err)
	}
	if name != "20260830T000000Z" {
		t.Fatalf("selected %q, want the newest backup's bare name", name)
	}
}

// TestSelectFDBBackupChecksTheWindowForTheZeroTime proves the selection no
// longer reads an explicit target as an absent one. The context carries no
// Docker client, so a drill that reaches the describe exec panics; recovering
// that panic is the observation that the window check ran. Before the target
// became a pointer this returned nil and the drill went on to restore the
// latest point.
func TestSelectFDBBackupChecksTheWindowForTheZeroTime(t *testing.T) {
	zeroInstant := time.Time{}
	drill := &restoreDrillCtx{
		Cfg:           &config.Config{},
		Cli:           nil,
		RunID:         "run",
		YBPass:        "pass",
		YBRunKey:      "",
		FDBTargetTime: &zeroInstant,
	}

	reached := false
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Logf("window check reached the describe exec and panicked on the absent client: %v", recovered)
				reached = true
			}
		}()
		if _, err := selectFDBBackup(
			context.Background(), drill, "tack-rtfdb-run", []string{"backups/20260830T000000Z"}); err != nil {
			t.Logf("window check reached the describe exec and errored: %v", err)
			reached = true
		}
	}()

	if !reached {
		t.Fatal("an explicitly named zero instant was treated as no target and skipped the window check")
	}
}
