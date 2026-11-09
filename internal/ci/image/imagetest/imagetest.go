// Package imagetest builds the image command for tests.
package imagetest

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const (
	commandPackage = "goodkind.io/tack/cmd/ci/image"
	linkName       = "ci-image"
)

var commandPath string

// RunTests builds ci-image and runs the calling package's tests.
//
// RunTests uses this sequence:
//
//  1. It builds the command once in a temporary directory.
//  2. It calls m.Run.
//  3. It removes the directory.
//
// The function returns the test exit code. It returns 1 for these failures:
//
//   - It cannot create the directory.
//   - It cannot build the command.
//   - It cannot remove the directory.
//
// Tests use Prepare to run the command.
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

	commandPath = filepath.Join(directory, linkName)

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

// Command creates a ci-image command from a context.
//
// The function returns an *[exec.Cmd] with the literal program and
// arguments.
//
//	func digestCommand(ctx context.Context) *exec.Cmd {
//		return exec.CommandContext(ctx, "./ci-image", "digest")
//	}
type Command func(ctx context.Context) *exec.Cmd

// Prepare sets up a ci-image command in directory.
//
// The calling package must call [RunTests] from TestMain before using
// Prepare. Prepare creates a hard link to the executable under the name
// ci-image. If that name already exists, it must refer to the same file.
// Prepare fails the test if RunTests did not build the executable or if it
// cannot create or verify the link.
//
// Prepare calls build with t.Context() and sets the command's Dir to
// directory. The caller can set Env before running the command.
//
//	command := imagetest.Prepare(t, t.TempDir(), digestCommand)
//	command.Env = environment
//	output, err := command.CombinedOutput()
//
// For example, a test can execute help and inspect its output:
//
//	helpCommand := func(ctx context.Context) *exec.Cmd {
//		return exec.CommandContext(ctx, "./ci-image", "--help")
//	}
//
//	command := imagetest.Prepare(t, t.TempDir(), helpCommand)
//	output, err := command.CombinedOutput()
//	if err != nil {
//		t.Fatal(err)
//	}
//	t.Log(string(output))
func Prepare(t *testing.T, directory string, build Command) *exec.Cmd {
	t.Helper()

	if commandPath == "" {
		t.Fatal("imagetest.Prepare needs RunTests to build the executable first")
	}

	link := filepath.Join(directory, linkName)

	err := os.Link(commandPath, link)
	if errors.Is(err, fs.ErrExist) {
		requireSameFile(t, commandPath, link)
	} else if err != nil {
		t.Fatalf("link %s into %s: %v", linkName, directory, err)
	}

	command := build(t.Context())
	command.Dir = directory

	return command
}

// requireSameFile fails the test if it cannot stat either path or if built
// and existing do not refer to the same file.
func requireSameFile(t *testing.T, built, existing string) {
	t.Helper()

	builtInfo, err := os.Stat(built)
	if err != nil {
		t.Fatalf("stat %s: %v", built, err)
	}

	existingInfo, err := os.Stat(existing)
	if err != nil {
		t.Fatalf("stat %s: %v", existing, err)
	}

	if !os.SameFile(builtInfo, existingInfo) {
		t.Fatalf("%s exists and is not the built executable", existing)
	}
}
