//go:build linux || darwin

package main_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestVerifyNativeRunnerCommand(t *testing.T) {
	var info unix.Utsname
	if err := unix.Uname(&info); err != nil {
		t.Fatal(err)
	}
	machine := unix.ByteSliceToString(info.Machine[:])
	matching := ""
	if machine == "x86_64" {
		matching = "amd64"
	} else if machine == "aarch64" {
		matching = "arm64"
	}
	for _, test := range []struct {
		name, architecture, expected string
		success                      bool
	}{
		{"missing", "", "IMAGE_ARCH", false},
		{"unsupported", "386", "unsupported image architecture", false},
		{"amd64", "amd64", machine, matching == "amd64"},
		{"arm64", "arm64", machine, matching == "arm64"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.CommandContext(t.Context(), commandPath, "verify-native-runner")
			command.Env = []string{}
			if test.name != "missing" {
				command.Env = append(command.Env, "IMAGE_ARCH="+test.architecture)
			}
			output, err := command.CombinedOutput()
			if test.success {
				if err != nil || string(output) != "Native image runner: "+machine+"\n" {
					t.Fatalf("output = %q, error = %v", output, err)
				}
			} else if err == nil || !strings.Contains(string(output), test.expected) {
				t.Fatalf("output = %q, error = %v", output, err)
			}
		})
	}
}

func TestVerifyNativeRunnerOutputFailure(t *testing.T) {
	var info unix.Utsname
	if err := unix.Uname(&info); err != nil {
		t.Fatal(err)
	}
	machine := unix.ByteSliceToString(info.Machine[:])
	var architecture string
	switch machine {
	case "x86_64":
		architecture = "amd64"
	case "aarch64":
		architecture = "arm64"
	default:
		t.Skipf("host machine %q does not match an accepted native runner pair", machine)
	}
	path := filepath.Join(t.TempDir(), "stdout")
	if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})
	command := exec.CommandContext(t.Context(), commandPath, "verify-native-runner")
	command.Env = []string{"IMAGE_ARCH=" + architecture}
	command.Stdout = output
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err == nil {
		t.Fatal("expected output write failure")
	}
	if !strings.Contains(stderr.String(), "write native runner output") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
