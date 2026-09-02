package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/config"
)

// launchFixture is a throwaway directory laid out the way the scratch
// container's is, so the launch program can be run for real. Asserting its text
// would prove nothing about the case these tests exist for: a destination that
// closes the quote it was interpolated into and runs as shell syntax.
type launchFixture struct {
	dir   string
	files fdbRestoreFiles
}

func newLaunchFixture(t *testing.T) launchFixture {
	t.Helper()
	dir := t.TempDir()
	return launchFixture{
		dir: dir,
		files: fdbRestoreFiles{
			Log:     filepath.Join(dir, "restore.log"),
			Exit:    filepath.Join(dir, "restore.rc"),
			Partial: filepath.Join(dir, "restore.rc.partial"),
		},
	}
}

// launch runs the argv the drill hands the container and waits for the
// backgrounded group to publish its exit status, returning the log and that
// status.
func (f launchFixture) launch(t *testing.T, restore []string) (logText, exitStatus string) {
	t.Helper()
	argv := fdbRestoreLaunchArgv(f.files, restore)
	if out, err := exec.CommandContext(t.Context(), argv[0], argv[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("launch exec: %v\n%s", err, out)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		status, err := os.ReadFile(f.files.Exit)
		if err == nil {
			body, readErr := os.ReadFile(f.files.Log)
			if readErr != nil {
				t.Fatalf("read restore log: %v", readErr)
			}
			return string(body), strings.TrimSpace(string(status))
		}
		if time.Now().After(deadline) {
			t.Fatalf("the launched group never published an exit status: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestLaunchFDBRestorePassesTheDestinationAsData is the defect this file exists
// for. The destination is the blobstore URL, and the backup name inside it is
// read out of whatever objects the bucket holds, so it is the attacker's half
// of the string. Interpolated into a single-quoted shell word it only had to
// carry one quote to close it and run the rest as shell, inside a container
// holding the object store's credentials. Passed as a positional parameter it
// reaches the program as one argument, whatever it contains.
func TestLaunchFDBRestorePassesTheDestinationAsData(t *testing.T) {
	fixture := newLaunchFixture(t)
	canary := filepath.Join(fixture.dir, "canary")
	dest := "blobstore://key:secret@host:8333/rt20260830';touch " + canary +
		";'?bucket=tack-backups&secure_connection=0"

	logText, exitStatus := fixture.launch(t, []string{"printf", "[%s]\n", dest})

	if _, err := os.Stat(canary); err == nil {
		t.Fatal("the destination ran as shell: it created the canary file")
	}
	if exitStatus != "0" {
		t.Fatalf("exit status = %q, want 0", exitStatus)
	}
	if logText != "["+dest+"]\n" {
		t.Fatalf("the destination did not reach the command verbatim as one argument:\n got %q\nwant %q",
			logText, "["+dest+"]\n")
	}
}

// TestLaunchFDBRestoreKeepsASpacedDestinationOneArgument proves the vector
// survives a destination that would otherwise split into several arguments,
// which is the same defect without the quote.
func TestLaunchFDBRestoreKeepsASpacedDestinationOneArgument(t *testing.T) {
	fixture := newLaunchFixture(t)
	dest := "blobstore://key:secret@host:8333/rt 2026 08 30?bucket=tack-backups"

	logText, exitStatus := fixture.launch(t, []string{"printf", "[%s]\n", dest})

	if exitStatus != "0" {
		t.Fatalf("exit status = %q, want 0", exitStatus)
	}
	if logText != "["+dest+"]\n" {
		t.Fatalf("the destination was split into more than one argument: got %q", logText)
	}
}

// TestLaunchFDBRestorePublishesTheCommandsExitStatus proves the shell still
// does the one job it is there for. The restore runs in the background so the
// exec can return and the watch can poll, and the status the watch reads has to
// be the command's own.
func TestLaunchFDBRestorePublishesTheCommandsExitStatus(t *testing.T) {
	fixture := newLaunchFixture(t)

	logText, exitStatus := fixture.launch(t, []string{"sh", "-c", "echo working; exit 3"})

	if exitStatus != "3" {
		t.Fatalf("exit status = %q, want the command's own 3", exitStatus)
	}
	if !strings.Contains(logText, "working") {
		t.Fatalf("the log must carry the command's output, got %q", logText)
	}
	if _, err := os.Stat(fixture.files.Partial); err == nil {
		t.Fatal("the partial status file must be moved into place, not left behind")
	}
}

// TestLaunchFDBRestoreAbortsWhenTheStaleStatusCannotBeCleared is the silent
// success the clear's guard exists for. The watch decides a restore finished by
// reading the status file, so a launch that could not clear the previous run's
// status and started anyway hands the watch that run's answer as this one's,
// which is a restore reporting an outcome it never produced. Here a directory
// stands where the status file goes, which `rm -f` refuses; the launch must
// exit non-zero and the restore must not start.
func TestLaunchFDBRestoreAbortsWhenTheStaleStatusCannotBeCleared(t *testing.T) {
	fixture := newLaunchFixture(t)
	if err := os.WriteFile(fixture.files.Log, []byte("the previous restore's output\n"), 0o600); err != nil {
		t.Fatalf("stage the previous restore log: %v", err)
	}
	if err := os.Mkdir(fixture.files.Exit, 0o755); err != nil {
		t.Fatalf("stage an unremovable status name: %v", err)
	}
	canary := filepath.Join(fixture.dir, "canary")

	argv := fdbRestoreLaunchArgv(fixture.files, []string{"touch", canary})
	out, err := exec.CommandContext(t.Context(), argv[0], argv[1:]...).CombinedOutput()

	if err == nil {
		t.Fatalf("a launch whose clear failed must exit non-zero, got output %q", out)
	}
	// The guard exits before the group is forked, so the restore never runs.
	// A launch that stepped over the failed clear forks it immediately, so a
	// short window is enough to catch one that did.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(canary); statErr == nil {
			t.Fatal("the restore started even though the previous run's status could not be cleared")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestFDBRestoreCommandCarriesTheDestinationUnaltered locks that the engine
// vector hands fdbrestore the destination as one argument with no quoting of
// its own, because the quoting is what the shell would have had to undo.
func TestFDBRestoreCommandCarriesTheDestinationUnaltered(t *testing.T) {
	dest := "blobstore://key:secret@host:8333/rt'20260830?bucket=tack-backups"

	command, err := fdbRestoreCommand(dest, nil)
	if err != nil {
		t.Fatalf("fdbRestoreCommand: %v", err)
	}

	found := false
	for i, arg := range command {
		if arg == "-r" && i+1 < len(command) {
			found = true
			if command[i+1] != dest {
				t.Fatalf("-r argument = %q, want the destination unaltered %q", command[i+1], dest)
			}
		}
	}
	if !found {
		t.Fatalf("the restore vector names no destination: %v", command)
	}
}

// TestLaunchArgvRunsTheBuiltRestoreVector is the seam between the two halves of
// the FoundationDB restore. The vector is built with the operator's target and
// checked against the backup's restorable window; the launch has to run that
// vector. A launch that assembled its own command from the destination would
// drop the target and restore the latest point instead, and report success
// doing it, which is the failure the target exists to prevent.
func TestLaunchArgvRunsTheBuiltRestoreVector(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	restore, err := fdbRestoreCommand(drillDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand: %v", err)
	}

	argv := fdbRestoreLaunchArgv(drillFDBRestoreFiles(), restore)

	if len(argv) < len(restore) {
		t.Fatalf("the launch dropped the restore vector: %q", argv)
	}
	if tail := argv[len(argv)-len(restore):]; !slices.Equal(tail, restore) {
		t.Fatalf("the launch does not run the vector it was given:\n got %q\nwant %q", tail, restore)
	}
	for _, want := range []string{"--timestamp", "2026/08/30.01:05:00+0000", "--orig-cluster-file"} {
		if !slices.Contains(argv, want) {
			t.Fatalf("the operator's target did not reach the launched command, %q missing: %q", want, argv)
		}
	}
}

// TestParseFDBRestoreExitRejectsWhatItCannotRead proves the probe that tells
// the wait a restore finished never guesses. An answer nobody could read must
// not pass for a restore that is still running.
func TestParseFDBRestoreExitRejectsWhatItCannotRead(t *testing.T) {
	for _, out := range []string{"", "  ", "exited", "exited x", "finished 0", "0"} {
		t.Run(out, func(t *testing.T) {
			if _, _, err := parseFDBRestoreExit(out); err == nil {
				t.Fatalf("parsing %q as an exit status must fail", out)
			}
		})
	}
}

// TestParseFDBRestoreExitReadsTheProbe locks the two answers the probe gives.
func TestParseFDBRestoreExitReadsTheProbe(t *testing.T) {
	if code, done, err := parseFDBRestoreExit("running\n"); err != nil || done || code != 0 {
		t.Fatalf("running probe = (%d, %v, %v), want (0, false, nil)", code, done, err)
	}
	if code, done, err := parseFDBRestoreExit("exited 3\n"); err != nil || !done || code != 3 {
		t.Fatalf("exited probe = (%d, %v, %v), want (3, true, nil)", code, done, err)
	}
}

// fdbRestoreStatusWithURL is one tag's line the way foundationdb 7.4.6's
// `fdbrestore status` prints it: the progress counters, then "URL: <source
// container>", which is the blobstore URL the restore was started with,
// credentials included.
func fdbRestoreStatusWithURL(counters string) string {
	return counters + "  URL: " + drillDestURL + "  Range: ''-'\\xff'  AddPrefix: ''  RemovePrefix: ''  Version: 100731044\n"
}

// TestFDBRestoreStatusReadingCarriesNoCredentials is the leak this test
// exists for. The status the watch reads names the source container URL, and
// that URL carries the blobstore access key and secret. The reading has to come
// out of the exec with both gone, on the success path as well as the failure
// path, so nothing that later carries the reading into a log or an error can
// write them; and the counters the watch reads have to survive the redaction.
func TestFDBRestoreStatusReadingCarriesNoCredentials(t *testing.T) {
	cfg := &config.Config{BackupS3AccessKey: "drill-access", BackupS3SecretKey: "drill-secret"} // gitleaks:allow test placeholder
	counters := fdbStatusLine(7, 900, 4, 28672, 3)

	reading, err := fdbRestoreStatusReading(cfg, execResult{
		Stdout:   fdbRestoreStatusWithURL(counters),
		Stderr:   "",
		ExitCode: 0,
	})
	if err != nil {
		t.Fatalf("fdbRestoreStatusReading: %v", err)
	}

	for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
		if strings.Contains(reading, credential) {
			t.Fatalf("the status reading still carries %q: %q", credential, reading)
		}
	}
	if !strings.Contains(reading, "URL: blobstore://") {
		t.Fatalf("redaction must remove the credentials, not the reading: %q", reading)
	}
	progress := newFDBRestoreProgress()
	if !progress.observe(reading) {
		t.Fatalf("the redacted reading must still carry its counters: %q", reading)
	}
	for _, mark := range []string{"blocks=7", "blockstotal=900", "byteswritten=28672", "applyversionlag=3"} {
		if !strings.Contains(progress.summary(), mark) {
			t.Fatalf("counter %s did not survive the redaction, got %q", mark, progress.summary())
		}
	}

	_, err = fdbRestoreStatusReading(cfg, execResult{
		Stdout:   "",
		Stderr:   "ERROR: cannot reach " + drillDestURL,
		ExitCode: 1,
	})
	if err == nil {
		t.Fatal("a status that exited non-zero must be an error")
	}
	for _, credential := range []string{cfg.BackupS3AccessKey, cfg.BackupS3SecretKey} {
		if strings.Contains(err.Error(), credential) {
			t.Fatalf("the status failure still carries %q: %v", credential, err)
		}
	}
}
