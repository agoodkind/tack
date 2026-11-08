package image_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

func runUpstream(t *testing.T, directory string, environment ...string) (string, error) {
	t.Helper()
	command := exec.CommandContext(t.Context(), imagetest.Path(), "upstream-yugabyte")
	command.Dir = directory
	command.Env = environment
	output, err := command.CombinedOutput()
	return string(output), err
}

func TestUpstreamYugabyteAppendsResolvedImage(t *testing.T) {
	directory := t.TempDir()
	writeFile(t, filepath.Join(directory, "docker-compose.yml"), "x-yugabyte-image: &yugabyte_image yugabytedb/yugabyte:2024.test\n")
	outputFile := filepath.Join(directory, "github-output")
	writeFile(t, outputFile, "existing=value\n")
	output, err := runUpstream(t, directory, "GITHUB_OUTPUT="+outputFile)
	if err != nil {
		t.Fatalf("err = %v, output = %s", err, output)
	}
	content, err := os.ReadFile(outputFile)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if string(content) != "existing=value\nyugabyte_image=yugabytedb/yugabyte:2024.test\n" {
		t.Fatalf("output file = %q", content)
	}
	if !strings.Contains(output, "yugabytedb/yugabyte:2024.test") {
		t.Fatalf("console output = %s", output)
	}
}

func TestUpstreamYugabyteRefusesBadInputWithoutAppending(t *testing.T) {
	directory := t.TempDir()
	writeFile(t, filepath.Join(directory, "docker-compose.yml"), "x-yugabyte-image: &yugabyte_image other/image:1\n")
	outputFile := filepath.Join(directory, "github-output")
	for name, environment := range map[string][]string{
		"unset output file":  {},
		"unpinned Compose":   {"GITHUB_OUTPUT=" + outputFile},
		"unreadable Compose": {"GITHUB_OUTPUT=" + outputFile},
	} {
		t.Run(name, func(t *testing.T) {
			target := directory
			if name == "unreadable Compose" {
				target = t.TempDir()
			}
			output, err := runUpstream(t, target, environment...)
			if err == nil || strings.Count(output, "ci-image.failed") != 1 {
				t.Fatalf("expected one failure, err = %v, output = %s", err, output)
			}
			if _, statErr := os.Stat(outputFile); !os.IsNotExist(statErr) {
				t.Fatalf("output file exists after a refused command: %v", statErr)
			}
		})
	}
}
