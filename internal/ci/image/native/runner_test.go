//go:build linux || darwin

package native_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	amd64Architecture = "amd64"
	arm64Architecture = "arm64"
)

// TestMain calls RunTests to run the native runner tests.
//
// RunTests manages the executable for each subprocess in the package:
//
//   - It builds the command once before the tests start.
//   - It removes the build directory after the tests finish.
//   - It returns the exit code that TestMain passes to [os.Exit].
func TestMain(m *testing.M) {
	os.Exit(imagetest.RunTests(m))
}

func verifyNativeRunnerCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "verify-native-runner")
}

func machineType(t *testing.T) string {
	t.Helper()

	var info unix.Utsname

	err := unix.Uname(&info)
	if err != nil {
		t.Fatal(err)
	}

	return unix.ByteSliceToString(info.Machine[:])
}

func matchingArchitecture(machine string) string {
	switch machine {
	case "x86_64":
		return amd64Architecture
	case "aarch64":
		return arm64Architecture
	default:
		return ""
	}
}

// TestVerifyNativeRunnerCommand checks native runner validation and output.
//
// Each subprocess uses an isolated environment. The cases check these
// inputs:
//
//   - The command rejects a missing IMAGE_ARCH value.
//   - The command rejects an unsupported architecture.
//   - For amd64, the command succeeds only when the host machine type
//     matches.
//   - For arm64, the command succeeds only when the host machine type
//     matches.
func TestVerifyNativeRunnerCommand(t *testing.T) {
	machine := machineType(t)
	matching := matchingArchitecture(machine)

	for _, test := range []struct {
		name, architecture, expected string
		success                      bool
	}{
		{"missing", "", "IMAGE_ARCH", false},
		{"unsupported", "386", "unsupported image architecture", false},
		{amd64Architecture, amd64Architecture, machine, matching == amd64Architecture},
		{arm64Architecture, arm64Architecture, machine, matching == arm64Architecture},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				command := imagetest.Prepare(t, t.TempDir(), verifyNativeRunnerCommand)
				command.Env = []string{}

				if test.name != "missing" {
					command.Env = append(command.Env, "IMAGE_ARCH="+test.architecture)
				}

				output, err := command.CombinedOutput()
				if test.success {
					if err != nil || string(output) != "Native image runner: "+machine+"\n" {
						t.Fatalf("output = %q, error = %v", output, err)
					}

					return
				}

				if err == nil || !strings.Contains(string(output), test.expected) {
					t.Fatalf("output = %q, error = %v", output, err)
				}
			},
		)
	}
}

// TestVerifyNativeRunnerOutputFailure checks standard output failures.
//
// Each subprocess must exit with status 1 and report the write failure.
// Unsupported host machine types skip these cases:
//
//   - The command writes to a file descriptor that permits only reads.
//   - The command writes to a pipe after its reader closes.
func TestVerifyNativeRunnerOutputFailure(t *testing.T) {
	machine := machineType(t)

	architecture := matchingArchitecture(machine)
	if architecture == "" {
		t.Skipf("host machine %q does not match an accepted native runner pair", machine)
	}

	t.Run(
		"read-only file",
		func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "stdout")

			err := os.WriteFile(path, []byte(""), 0o600)
			if err != nil {
				t.Fatal(err)
			}

			output, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				closeErr := output.Close()
				if closeErr != nil {
					t.Error(closeErr)
				}
			})
			assertOutputFailure(t, architecture, output)
		},
	)

	t.Run(
		"closed pipe",
		func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}

			err = reader.Close()
			if err != nil {
				t.Fatal(err)
			}

			t.Cleanup(func() {
				closeErr := writer.Close()
				if closeErr != nil {
					t.Error(closeErr)
				}
			})
			assertOutputFailure(t, architecture, writer)
		},
	)
}

func assertOutputFailure(t *testing.T, architecture string, output *os.File) {
	t.Helper()

	command := imagetest.Prepare(t, t.TempDir(), verifyNativeRunnerCommand)
	command.Env = []string{"IMAGE_ARCH=" + architecture}
	command.Stdout = output

	var stderr bytes.Buffer

	command.Stderr = &stderr

	err := command.Run()
	if err == nil {
		t.Fatal("expected output write failure")
	}

	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("expected exit status 1, got %v", err)
	}

	if !strings.Contains(stderr.String(), "write native runner output") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
