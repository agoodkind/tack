//go:build darwin && integration && macrunnerfixture

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMacRunnerMakeGroup(t *testing.T) {
	for _, outcome := range []string{"flags", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			options, docker := macCommandFixture(t)
			repository, err := filepath.Abs("../..")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(repository, ".make"), filepath.Join(options.Root, ".make")); err != nil {
				t.Fatal(err)
			}
			arguments := []string{
				"-C", options.Root, "-I", repository, "-f", filepath.Join(repository, "Makefile"),
				"GO_MK_SKIP_FETCH=1", "test-search-group", "TEST_SEARCH_RUNNER=mac", "GROUP=G5A",
			}
			if outcome == "flags" {
				macMakeRefusals(t, arguments, options.Image, options.EvidenceDirectory)
			}
			if err := os.Mkdir(filepath.Join(options.Root, "dist"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(repository, "dist/testenv"), filepath.Join(options.Root, "dist/testenv")); err != nil {
				t.Fatal(err)
			}
			if outcome == "flags" {
				macMakeRefusal(t, append(append([]string{}, arguments...), "MAC_RUNNER_IMAGE=latest", "MAC_RUNNER_EVIDENCE_DIR="+options.EvidenceDirectory), options.EvidenceDirectory, "require an immutable local image ID")
			}
			flags := "-count=2 -timeout=45s -tags=macrunnerfixture,runtimeaudit -run '^TestMacRunner(Flags|Skip|CacheMarker)$$' -skip '^TestMacRunnerSkip$$'"
			expected := 0
			if outcome == "failure" {
				flags = "-count=1 -timeout=45s -tags=macrunnerfixture,runtimeaudit -run '^TestMacRunnerFailure$$'"
				expected = 1
			}
			arguments = append(arguments, "MAC_RUNNER_IMAGE="+options.Image, "MAC_RUNNER_EVIDENCE_DIR="+options.EvidenceDirectory, "SEARCH_G5A_ARGS="+flags)
			command := exec.CommandContext(t.Context(), "make", arguments...)
			command.Stdout, command.Stderr = os.Stdout, os.Stderr
			err = command.Run()
			if expected == 0 && err != nil {
				t.Fatalf("native Makefile group failed: %v", err)
			}
			var exitError *exec.ExitError
			if expected != 0 && (!errors.As(err, &exitError) || exitError.ExitCode() != 2) {
				t.Fatalf("make did not report the failed native test: %v", err)
			}
			macCommandEvidence(t, docker, options.EvidenceDirectory, expected, false)
			if expected != 0 {
				return
			}
			contents, err := os.ReadFile(filepath.Join(options.EvidenceDirectory, "runtime.log"))
			if err != nil {
				t.Fatal(err)
			}
			output := string(contents)
			if strings.Count(output, "MAC_RUNNER_FLAGS") != 2 || strings.Count(output, "MAC_RUNNER_CACHE_MARKER") != 2 || strings.Contains(output, "=== RUN   TestMacRunnerSkip") || !strings.Contains(output, "-test.timeout=45s") {
				t.Fatalf("native Makefile arguments did not select the required runtime behavior: %s", output)
			}
		})
	}
}

func macMakeRefusals(t *testing.T, arguments []string, image, directory string) {
	t.Helper()
	for _, test := range []struct {
		name      string
		arguments []string
		message   string
	}{
		{name: "mode", arguments: []string{"TEST_SEARCH_RUNNER=unknown"}, message: "TEST_SEARCH_RUNNER must be compose or mac"},
		{name: "image", message: "set MAC_RUNNER_IMAGE"},
		{name: "evidence", arguments: []string{"MAC_RUNNER_IMAGE=" + image}, message: "set MAC_RUNNER_EVIDENCE_DIR"},
		{name: "executable", arguments: []string{"MAC_RUNNER_IMAGE=" + image, "MAC_RUNNER_EVIDENCE_DIR=" + directory}, message: "make build BINARY=testenv CMD=./cmd/testenv"},
	} {
		t.Run(test.name, func(t *testing.T) {
			commandArguments := append([]string{}, arguments...)
			commandArguments = append(commandArguments, test.arguments...)
			macMakeRefusal(t, commandArguments, directory, test.message)
		})
	}
}

func macMakeRefusal(t *testing.T, arguments []string, directory, message string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "make", arguments...)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 2 || !strings.Contains(string(output), message) {
		t.Fatalf("make refusal: error=%v output=%s", err, output)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("make refusal created runtime evidence: %v", err)
	}
}
