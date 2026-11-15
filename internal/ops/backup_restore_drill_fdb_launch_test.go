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

// TestLaunchFDBRestorePassesTheDestinationAsData checks that an attacker-controlled
// backup URL remains one argument. The payload would close an interpolated
// single-quoted shell word and create a file if the launch executed it as shell.
func TestLaunchFDBRestorePassesTheDestinationAsData(t *testing.T) {
	fixture := newLaunchFixture(t)
	canary := filepath.Join(fixture.dir, "canary")
	dest := "blobstore://key:secret@host:8333/rt20260830';printf injected >" + canary +
		";'?bucket=tack-backups&secure_connection=0"

	logText, exitStatus := fixture.launch(t, []string{"printf", "[%s]\n", dest})

	if _, err := os.Stat(canary); err == nil {
		t.Fatal("the destination executed as shell and created the canary file")
	}

	if exitStatus != "0" {
		t.Fatalf("exit status = %q, want 0", exitStatus)
	}

	if logText != "["+dest+"]\n" {
		t.Fatalf(
			"the command did not receive the complete destination as one argument:\n got %q\nwant %q",
			logText,
			"["+dest+"]\n",
		)
	}
}

// TestLaunchFDBRestoreKeepsASpacedDestinationOneArgument checks argument
// boundaries for a destination containing spaces.
func TestLaunchFDBRestoreKeepsASpacedDestinationOneArgument(t *testing.T) {
	fixture := newLaunchFixture(t)
	dest := "blobstore://key:secret@host:8333/rt 2026 08 30?bucket=tack-backups"

	logText, exitStatus := fixture.launch(t, []string{"printf", "[%s]\n", dest})

	if exitStatus != "0" {
		t.Fatalf("exit status = %q, want 0", exitStatus)
	}

	if logText != "["+dest+"]\n" {
		t.Fatalf("the command received multiple destination arguments: got %q", logText)
	}
}

// TestLaunchFDBRestorePublishesTheCommandsExitStatus checks that the background
// restore saves its own exit status and output for the polling operation.
func TestLaunchFDBRestorePublishesTheCommandsExitStatus(t *testing.T) {
	fixture := newLaunchFixture(t)

	logText, exitStatus := fixture.launch(t, []string{"sh", "-c", "echo working; exit 3"})

	if exitStatus != "3" {
		t.Fatalf("exit status = %q, want the command's own 3", exitStatus)
	}

	if !strings.Contains(logText, "working") {
		t.Fatalf("the log must include the command's output, got %q", logText)
	}

	if _, err := os.Stat(fixture.files.Partial); err == nil {
		t.Fatal("the launch must rename the partial status file to the final status path")
	}
}

// TestLaunchFDBRestoreAbortsWhenTheStaleStatusCannotBeCleared checks that a
// failed removal prevents the restore from starting with a previous exit status.
// A directory at the status path makes rm -f fail.
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
		t.Fatalf("stage a directory at the status path: %v", err)
	}

	canary := filepath.Join(fixture.dir, "canary")

	argv := fdbRestoreLaunchArgv(fixture.files, []string{"sh", "-c", "printf started >\"$1\"", "restore-canary", canary})

	output, err := exec.CommandContext(t.Context(), argv[0], argv[1:]...).CombinedOutput()
	if err == nil {
		t.Fatalf("a launch with a failed status removal must exit nonzero, got output %q", output)
	}

	// The removal guard must exit before starting the background restore.
	// Polling detects a restore that starts despite the failed removal.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(canary); statErr == nil {
			t.Fatal(
				"the restore started even though the launch could not remove the previous status",
			)
		}

		time.Sleep(5 * time.Millisecond)
	}
}

// TestFDBRestoreCommandPreservesTheDestination checks that fdbrestore receives
// the destination as one argument without additional shell quoting.
func TestFDBRestoreCommandPreservesTheDestination(t *testing.T) {
	dest := "blobstore://key:secret@host:8333/rt'20260830?bucket=tack-backups"

	command, err := fdbRestoreCommand(dest, nil)
	if err != nil {
		t.Fatalf("fdbRestoreCommand: %v", err)
	}

	found := false

	for argumentIndex, argument := range command {
		if argument == "-r" && argumentIndex+1 < len(command) {
			found = true
			if command[argumentIndex+1] != dest {
				t.Fatalf("-r argument = %q, want the destination unaltered %q", command[argumentIndex+1], dest)
			}
		}
	}

	if !found {
		t.Fatalf("the restore command does not specify a destination: %v", command)
	}
}

// TestLaunchArgvRunsTheBuiltRestoreVector checks that the launch executes the
// validated command with the operator's target time and original cluster file.
func TestLaunchArgvRunsTheBuiltRestoreVector(t *testing.T) {
	target := time.Date(2026, 8, 30, 1, 5, 0, 0, time.UTC)
	restore, err := fdbRestoreCommand(drillDestURL, &target)
	if err != nil {
		t.Fatalf("fdbRestoreCommand: %v", err)
	}

	argv := fdbRestoreLaunchArgv(drillFDBRestoreFiles(), restore)

	if len(argv) < len(restore) {
		t.Fatalf("the launch omitted restore arguments: %q", argv)
	}

	if restoreArguments := argv[len(argv)-len(restore):]; !slices.Equal(restoreArguments, restore) {
		t.Fatalf(
			"the launch does not execute the supplied restore arguments:\n got %q\nwant %q",
			restoreArguments,
			restore,
		)
	}

	for _, want := range []string{"--timestamp", "2026/08/30.01:05:00+0000", "--orig-cluster-file"} {
		if !slices.Contains(argv, want) {
			t.Fatalf(
				"the launched command omitted target argument %q: %q",
				want,
				argv,
			)
		}
	}
}
