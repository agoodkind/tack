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

// hostileDestURL tests shell syntax in a backup name. Production backup
// selection reads names from bucket objects, which determine their trust.
// The complete destination URL includes the blobstore access key and secret.
const hostileDestURL = "blobstore://drill-access:drill-secret@fdb-blobstore-host:8333/" + // gitleaks:allow test placeholder
	"20260830T000000Z'; touch /tmp/pwned; '?bucket=tack-backups"

// describeWithTimestamps reproduces the original FoundationDB 7.4.6
// fdbbackup describe --version-timestamps output observation. That observation
// used format strings from the compiled fdbbackup binary.
const describeWithTimestamps = "URL: " + drillDestURL + "\n" +
	"Restorable: true\n" +
	"Partitioned logs: false\n" +
	"SnapshotBytes: 1048576\n" +
	"MinLogBeginVersion:      100000000 (2026/08/30.00:00:00+0000)\n" +
	"ContiguousLogEndVersion: 100731044 (2026/08/30.01:10:03+0000)\n" +
	"MaxLogEndVersion:        100731044 (2026/08/30.01:10:03+0000)\n" +
	"MinRestorableVersion:    100700000 (2026/08/30.01:00:00+0000)\n" +
	"MaxRestorableVersion:    100731044 (2026/08/30.01:10:03+0000)\n"

// TestFDBRestoreCommandRestoresLatestByDefault checks the command used before
// point-in-time restore. An omitted target must preserve that command without
// adding a timestamp or source cluster argument.
func TestFDBRestoreCommandRestoresLatestByDefault(t *testing.T) {
	got, err := fdbRestoreCommand(drillDestURL, nil)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with no target: %v", err)
	}

	want := []string{
		"fdbrestore",
		"start",
		"--dest-cluster-file",
		"/var/fdb/fdb.cluster",
		"-r",
		drillDestURL,
		"--waitfordone",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("default restore command changed:\n got %q\nwant %q", got, want)
	}
}

// TestFDBRestoreCommandBoundsNoTotalRunTime checks that latest and targeted
// restore commands omit the timeout wrapper. Restore duration grows with the
// dataset; a fixed total limit could stop a healthy large restore. The drill
// limits inactivity instead of total restore work.
func TestFDBRestoreCommandBoundsNoTotalRunTime(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	for name, targetTime := range map[string]*time.Time{"latest": nil, "targeted": &target} {
		t.Run(
			name,
			func(t *testing.T) {
				got, err := fdbRestoreCommand(drillDestURL, targetTime)
				if err != nil {
					t.Fatalf("fdbRestoreCommand: %v", err)
				}

				if slices.Contains(got, "timeout") {
					t.Fatalf("the restore must not carry a total-work budget: %q", got)
				}
			},
		)
	}
}

// TestFDBRestoreCommandCarriesTargetAndSourceCluster checks the target
// timestamp and source cluster file arguments. The engine uses source
// metadata to convert time to a version, while the destination argument
// selects the scratch cluster.
func TestFDBRestoreCommandCarriesTargetAndSourceCluster(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)

	got, err := fdbRestoreCommand(drillDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand with a whole-second target: %v", err)
	}

	want := []string{
		"fdbrestore",
		"start",
		"--dest-cluster-file",
		"/var/fdb/fdb.cluster",
		"-r",
		drillDestURL,
		"--waitfordone",
		"--timestamp",
		"2026/08/30.01:05:00+0000",
		"--orig-cluster-file",
		"/tack-orig-fdb/fdb.cluster",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("targeted restore command changed:\n got %q\nwant %q", got, want)
	}

	// FoundationDB defaults to /etc/foundationdb/fdb.cluster when a command
	// omits its cluster-file argument. The live cluster file must use a different
	// path inside the scratch container.
	if strings.Contains(fdbOrigClusterFilePath, "/etc/foundationdb/") {
		t.Fatalf(
			"source cluster file must not occupy FoundationDB's default path: %q",
			fdbOrigClusterFilePath,
		)
	}
}

// TestFDBCommandsCarryTheDestinationAsOneArgument checks that each command
// keeps the hostile destination URL in one argument and omits its own shell.
// The URL includes credentials and a backup name from bucket objects. A quote
// in either could inject commands into constructed shell text in a container
// with access to the live cluster.
//
// The drill executes the describe vector directly. It starts restore in the
// background through a shell to monitor progress, using positional parameters
// for the engine arguments. A vector with its own sh -c would interpret the
// destination as program text again.
func TestFDBCommandsCarryTheDestinationAsOneArgument(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	restore, err := fdbRestoreCommand(hostileDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand: %v", err)
	}

	describe := fdbDescribeCommand(hostileDestURL)

	for name, command := range map[string][]string{"restore": restore, "describe": describe} {
		t.Run(
			name,
			func(t *testing.T) {
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
					t.Fatalf(
						"destination URL must be exactly one whole argument, appeared %d times: %q",
						whole,
						command,
					)
				}
			},
		)
	}
}

// TestFDBRestoreCommandRefusesSubSecondTarget checks rejection at command
// construction before the engine can truncate an operator's fractional
// second. Flag parsing rejects that precision earlier; command construction
// must also enforce the engine's whole-second timestamp format.
func TestFDBRestoreCommandRefusesSubSecondTarget(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 900000000, time.UTC)

	got, err := fdbRestoreCommand(drillDestURL, &target)
	if err == nil {
		t.Fatalf("a sub-second target must not assemble a restore: %q", got)
	}

	if got != nil {
		t.Fatalf("a refused target must yield no command: %q", got)
	}

	for _, want := range []string{
		"2026-08-30T01:05:00.9Z",
		fdbTimestampArgForm,
		"2026/08/30.01:05:00+0000",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal must name %q so the operator can pick a moment: %v", want, err)
		}
	}
}

// TestFDBTargetTimeMountIsReadOnlySourceCluster checks that the live cluster
// file mount uses the restore's source path and the read-only option.
func TestFDBTargetTimeMountIsReadOnlySourceCluster(t *testing.T) {
	if !strings.HasSuffix(fdbOrigClusterMount, ":"+fdbOrigClusterFilePath+":ro") {
		t.Fatalf(
			"source cluster mount must be read-only at the named path: %q",
			fdbOrigClusterMount,
		)
	}
}

// TestFDBDescribeCommandReadsWindowFromSourceCluster checks the source
// cluster argument, --version-timestamps and the command timeout. Comparing
// the window with an operator's target requires wall-clock times.
//
// The drill does not monitor this exec; a source that accepts a connection
// without answering must not block it indefinitely. The original timeout
// rationale treated metadata work as independent of dataset size, unlike
// restore work. TACK-609 later recorded timestamp-conversion failures after
// hundreds of snapshots.
func TestFDBDescribeCommandReadsWindowFromSourceCluster(t *testing.T) {
	got := fdbDescribeCommand(drillDestURL)

	want := []string{
		"timeout",
		strconv.Itoa(fdbDescribeTimeoutSeconds),
		"fdbbackup",
		"describe",
		"-d",
		drillDestURL,
		"-C",
		fdbOrigClusterFilePath,
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
		t.Fatalf(
			"window = %s .. %s, want %s .. %s",
			formatFDBTime(window.Min),
			formatFDBTime(window.Max),
			formatFDBTime(wantMin),
			formatFDBTime(wantMax),
		)
	}
}

// TestFDBRestorableWindowRejectsUnusableDescribe checks representative
// outputs that cannot establish a window. Each case must return its expected
// error. An unreadable window must not permit a silent restore of the latest
// data instead of the requested time.
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
			// Describe output echoes the destination URL before the verdict. A backup
			// name containing the verdict text must not establish restorability.
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
		t.Run(
			test.name,
			func(t *testing.T) {
				_, err := fdbRestorableWindowFromDescribe(test.describe)
				if err == nil {
					t.Fatal("expected an unusable describe to fail")
				}

				if !strings.Contains(err.Error(), test.want) {
					t.Fatalf("error must explain the failure, want %q in: %v", test.want, err)
				}
			},
		)
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

// TestAssertTargetWithinWindowRefusesOutsideAndNamesTheWindow checks refusal
// of targets before and after the window. The error must include the target
// and both bounds rather than silently substituting the latest point.
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
		t.Run(
			name,
			func(t *testing.T) {
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
						t.Fatalf(
							"refusal must name %q so the operator can pick a reachable moment: %v",
							want,
							err,
						)
					}
				}
			},
		)
	}
}

// TestParseFDBTargetTime checks that the accepted timestamp forms identify
// the same instant, including the form with a non-UTC offset.
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
		t.Run(
			test.name,
			func(t *testing.T) {
				got, err := parseFDBTargetTime(test.input)
				if err != nil {
					t.Fatalf("parseFDBTargetTime(%q): %v", test.input, err)
				}

				if !got.Equal(want) {
					t.Fatalf(
						"parseFDBTargetTime(%q) = %s, want %s",
						test.input,
						formatFDBTime(got),
						formatFDBTime(want),
					)
				}
			},
		)
	}
}

// TestParseFDBTargetTimeRefusesAmbiguousInput checks rejection of inputs
// that omit a time zone or cannot parse. The parser must not guess the
// operator's intended restore time.
func TestParseFDBTargetTimeRefusesAmbiguousInput(t *testing.T) {
	for _, input := range []string{"yesterday", "2026-08-30", "2026-08-30T01:05:00", ""} {
		if _, err := parseFDBTargetTime(input); err == nil {
			t.Fatalf("parseFDBTargetTime(%q) must fail rather than guess a moment", input)
		}
	}
}

// TestParseFDBTargetTimeRefusesSubSecondInput checks precision that
// FoundationDB's --timestamp cannot express. Go's time.Parse accepts a
// fraction after seconds even when the layout omits it. Both accepted forms
// can parse a precision that the engine would truncate to the preceding
// whole second instead of the requested instant.
func TestParseFDBTargetTimeRefusesSubSecondInput(t *testing.T) {
	for _, input := range []string{
		"2026-08-30T01:05:00.9Z",
		"2026-08-29T21:05:00.000000001-04:00",
		"2026/08/30.01:05:00.9+0000",
	} {
		t.Run(
			input,
			func(t *testing.T) {
				got, err := parseFDBTargetTime(input)
				if err == nil {
					t.Fatalf(
						"parseFDBTargetTime(%q) = %s, must refuse a precision the restore drops",
						input,
						formatFDBTime(got),
					)
				}

				if !strings.Contains(err.Error(), fdbTimestampArgForm) {
					t.Fatalf("refusal must name the form a target has to fit: %v", err)
				}
			},
		)
	}
}

// TestParseFDBTargetTimeKeepsTheZeroTimeExplicit checks that a parsed zero
// instant remains an explicit target. A pointer distinguishes that instant
// from an omitted target, which selects the latest restorable point.
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

// TestSelectFDBBackupSkipsTheWindowCheckByDefault checks that an omitted
// target selects the newest backup, preserving the behavior before
// point-in-time restore. The fixture omits the Docker client; selection must
// succeed without executing a describe or querying the source cluster.
func TestSelectFDBBackupSkipsTheWindowCheckByDefault(t *testing.T) {
	drill := &restoreDrillCtx{
		Cfg:           &config.Config{},
		Cli:           nil,
		RunID:         "run",
		YBPass:        "pass",
		YBRunKey:      "",
		FDBTargetTime: nil,
	}

	name, err := selectFDBBackup(
		context.Background(),
		drill,
		"tack-rtfdb-run",
		[]string{"backups/20260829T000000Z", "backups/20260830T000000Z"},
	)
	if err != nil {
		t.Fatalf("a drill with no target time must not check a window: %v", err)
	}

	if name != "20260830T000000Z" {
		t.Fatalf("selected %q, want the newest backup's bare name", name)
	}
}

// TestSelectFDBBackupChecksTheWindowForTheZeroTime checks that an explicit
// zero instant does not take the omitted-target path. The fixture omits the
// Docker client and accepts either an error or a recovered panic; either
// prevents the successful default selection. It does not assert the exact
// failing operation.
//
// Before the target became a pointer, the zero value selected the latest
// point without checking a window.
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
				t.Logf(
					"window check reached the describe exec and panicked on the absent client: %v",
					recovered,
				)
				reached = true
			}
		}()
		if _, err := selectFDBBackup(
			context.Background(),
			drill,
			"tack-rtfdb-run",
			[]string{"backups/20260830T000000Z"},
		); err != nil {
			t.Logf("window check reached the describe exec and errored: %v", err)
			reached = true
		}
	}()

	if !reached {
		t.Fatal(
			"an explicitly named zero instant was treated as no target and skipped the window check",
		)
	}
}
