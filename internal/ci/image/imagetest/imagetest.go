// Package imagetest builds the image command for tests.
package imagetest

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const commandPackage = "goodkind.io/tack/cmd/ci/image"

var commandPath string

// RunTests builds the image command, runs the calling package's tests,
// and removes the build directory. It returns the exit code for TestMain.
func RunTests(m *testing.M) int {
	directory, err := os.MkdirTemp("", "ci-image-test-")
	if err != nil {
		slog.Error("imagetest.directory_failed", slog.Any("err", err))

		return 1
	}

	commandPath = filepath.Join(directory, "ci-image")

	build := exec.CommandContext(
		context.Background(),
		"go", "build", "-buildvcs=false", "-o", commandPath, commandPackage,
	)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr

	err = build.Run()
	if err != nil {
		slog.Error("imagetest.build_failed", slog.Any("err", err))

		removeErr := os.RemoveAll(directory)
		if removeErr != nil {
			slog.Error("imagetest.cleanup_failed", slog.Any("err", removeErr))
		}

		return 1
	}

	result := m.Run()

	err = os.RemoveAll(directory)
	if err != nil {
		slog.Error("imagetest.cleanup_failed", slog.Any("err", err))

		return 1
	}

	slog.Info("imagetest.finished", slog.Int("exit_code", result))

	return result
}

// Path returns the path to the image command built by RunTests.
func Path() string {
	return commandPath
}
