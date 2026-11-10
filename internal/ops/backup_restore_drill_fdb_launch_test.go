package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestLaunchFDBRestorePassesTheDestinationAsData checks the earlier shell
// injection defect. The blobstore URL includes a backup name from the bucket's
// objects, which an attacker can control. A quote in that name could close a
// single-quoted interpolation and execute shell syntax inside a container
// with object-store credentials. Positional parameters must preserve the
// destination as one argument regardless of its contents.
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
		t.Fatalf(
			"the destination did not reach the command verbatim as one argument:\n got %q\nwant %q",
			logText,
			"["+dest+"]\n",
		)
	}
}

// TestLaunchFDBRestoreKeepsASpacedDestinationOneArgument checks that spaces
// in the destination do not split the vector into extra arguments. It tests
// the argument-handling defect without a quote.
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

// TestLaunchFDBRestorePublishesTheCommandsExitStatus checks that the shell
// publishes the restore command's own status. Background execution lets the
// exec return while the watch polls that status.
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

// TestLaunchFDBRestoreAbortsWhenTheStaleStatusCannotBeCleared checks that a
// failed cleanup prevents launch. The watch uses the status file to detect
// completion; an old status could otherwise report an outcome that the
// current restore never produced.
//
// The fixture creates a directory at the status path. rm -f refuses to remove
// it. The launch must exit with a nonzero status without starting the restore.
func TestLaunchFDBRestoreAbortsWhenTheStaleStatusCannotBeCleared(t *testing.T) {
	fixture := newLaunchFixture(t)
	if err := os.WriteFile(
		fixture.files.Log,
		[]byte("the previous restore's output\n"),
		0o600,
	); err != nil {
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

	// The guard exits before forking the command group. An unguarded launch
	// would fork immediately after failed cleanup. The short observation window
	// checks whether that incorrect launch starts the restore.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(canary); statErr == nil {
			t.Fatal(
				"the restore started even though the previous run's status could not be cleared",
			)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// TestFDBRestoreCommandCarriesTheDestinationUnaltered checks that the engine
// vector supplies fdbrestore with one unchanged destination argument. The
// vector must not add quoting that a shell would need to remove.
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

// TestLaunchArgvRunsTheBuiltRestoreVector checks that launch executes the
// vector built for the operator's target after validation against the backup's
// restorable window. Rebuilding a command from the destination alone would
// omit the target, restore the latest point, and incorrectly report success.
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
		t.Fatalf(
			"the launch does not run the vector it was given:\n got %q\nwant %q",
			tail,
			restore,
		)
	}

	for _, want := range []string{"--timestamp", "2026/08/30.01:05:00+0000", "--orig-cluster-file"} {
		if !slices.Contains(argv, want) {
			t.Fatalf(
				"the operator's target did not reach the launched command, %q missing: %q",
				want,
				argv,
			)
		}
	}
}
