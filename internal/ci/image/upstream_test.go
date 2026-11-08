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
	for _, test := range []struct {
		name         string
		compose      string
		writeCompose bool
		setOutput    bool
		wantCause    string
	}{
		{
			name:         "unset output file",
			compose:      "x-yugabyte-image: &yugabyte_image yugabytedb/yugabyte:2024.test\n",
			writeCompose: true,
			wantCause:    "GITHUB_OUTPUT: required environment variable is unset",
		},
		{
			name:         "image outside the pinned repository",
			compose:      "x-yugabyte-image: &yugabyte_image other/image:1\n",
			writeCompose: true,
			setOutput:    true,
			wantCause:    "invalid or missing x-yugabyte-image value in docker-compose.yml",
		},
		{
			name:         "malformed Compose file",
			compose:      "x-yugabyte-image: [unclosed\n",
			writeCompose: true,
			setOutput:    true,
			wantCause:    "parse Compose file docker-compose.yml",
		},
		{
			name:      "missing Compose file",
			setOutput: true,
			wantCause: "no such file or directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			if test.writeCompose {
				writeFile(t, filepath.Join(directory, "docker-compose.yml"), test.compose)
			}
			outputFile := filepath.Join(directory, "github-output")
			var environment []string
			if test.setOutput {
				environment = append(environment, "GITHUB_OUTPUT="+outputFile)
			}
			output, err := runUpstream(t, directory, environment...)
			if err == nil {
				t.Fatalf("expected failure, output = %s", output)
			}
			if strings.Count(output, "ci-image.failed") != 1 {
				t.Fatalf("expected one ci-image.failed boundary event, output = %s", output)
			}
			if !strings.Contains(output, test.wantCause) {
				t.Fatalf("output does not contain the cause %q: %s", test.wantCause, output)
			}
			if strings.Contains(output, "Upstream Yugabyte image is") {
				t.Fatalf("command printed the image after a refusal: %s", output)
			}
			if _, statErr := os.Stat(outputFile); !os.IsNotExist(statErr) {
				t.Fatalf("output file exists after a refused command: %v", statErr)
			}
		})
	}
}
