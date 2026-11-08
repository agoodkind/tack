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

// RunTests builds the image command and runs the calling package's tests.
//
// It builds the command in a temporary directory, calls m.Run, and removes
// the directory. It returns the test exit code, or 1 if the build or
// cleanup fails. Tests can use Path to run the command.
//
//	func TestMain(m *testing.M) {
//		os.Exit(imagetest.RunTests(m))
//	}
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

// Path returns the filesystem path of the ci-image executable.
// RunTests builds the executable before it runs the tests. Path returns
// an empty string before RunTests starts.
//
// Tests can pass the path to [exec.CommandContext]:
//
//	command := exec.CommandContext(t.Context(), imagetest.Path(), arguments...)
func Path() string {
	return commandPath
}
