package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// launchFixture uses a temporary directory with the scratch container's file
// layout to execute the launch program. These tests exercise the injection
// case in which an interpolated destination could close a quote and execute
// shell syntax. A program-text assertion would not establish that behavior.
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

// launch runs the exec arguments that the drill supplies to the container.
// It waits for the background group to publish an exit status, then returns
// the log and status.
func (f launchFixture) launch(t *testing.T, restore []string) (logText, exitStatus string) {
	t.Helper()
	argv := fdbRestoreLaunchArgv(f.files, restore)
	if out, err := exec.CommandContext(
		t.Context(),
		argv[0],
		argv[1:]...,
	).CombinedOutput(); err != nil {
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
