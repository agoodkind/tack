package ops

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
