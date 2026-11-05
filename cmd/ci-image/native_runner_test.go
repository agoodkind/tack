//go:build linux || darwin

package main_test

import (
	"os/exec"
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
