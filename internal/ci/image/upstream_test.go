package image_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	pinnedCompose = "x-yugabyte-image: &yugabyte_image yugabytedb/yugabyte:2024.test\n"
	imagePrinted  = "Upstream Yugabyte image is"
)

func runUpstream(t *testing.T, directory string, environment ...string) (string, error) {
	t.Helper()

	command := exec.CommandContext(t.Context(), imagetest.Path(), "upstream-yugabyte")
	command.Dir = directory
	// A nil Env inherits the parent environment, including GITHUB_OUTPUT on CI.
	// A non-nil Env uses the listed variables, even when the slice is empty.
	command.Env = append([]string{}, environment...)
	output, err := command.CombinedOutput()

	return string(output), err
}

// TestUpstreamYugabyteAppendsResolvedImage checks that the command prints
// the Compose image and appends it to the existing GitHub output file.
func TestUpstreamYugabyteAppendsResolvedImage(t *testing.T) {
	directory := t.TempDir()
	outputFile := filepath.Join(directory, "github-output")

	writeFile(t, filepath.Join(directory, "docker-compose.yml"), pinnedCompose)
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

type upstreamRefusal struct {
	name         string
	compose      string
	writeCompose bool
	setOutput    bool
	wantCause    string
}

func upstreamRefusals() []upstreamRefusal {
	return []upstreamRefusal{
		{
			name:         "unset output file",
			compose:      pinnedCompose,
			writeCompose: true,
			setOutput:    false,
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
			name:         "missing Compose file",
			compose:      "",
			writeCompose: false,
			setOutput:    true,
			wantCause:    "no such file or directory",
		},
	}
}

// TestUpstreamYugabyteRefusesBadInputWithoutAppending checks that invalid
// input fails without printing the image or creating an output file.
func TestUpstreamYugabyteRefusesBadInputWithoutAppending(t *testing.T) {
	for _, test := range upstreamRefusals() {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			outputFile := filepath.Join(directory, "github-output")
			parentOutput := filepath.Join(t.TempDir(), "parent-github-output")

			t.Setenv("GITHUB_OUTPUT", parentOutput)

			if test.writeCompose {
				writeFile(t, filepath.Join(directory, "docker-compose.yml"), test.compose)
			}

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

			if strings.Contains(output, imagePrinted) {
				t.Fatalf("command printed the image after a refusal: %s", output)
			}

			_, statErr := os.Stat(outputFile)
			if !os.IsNotExist(statErr) {
				t.Fatalf("output file exists after a refused command: %v", statErr)
			}

			_, statErr = os.Stat(parentOutput)
			if !os.IsNotExist(statErr) {
				t.Fatalf("inherited GITHUB_OUTPUT file exists after a refused command: %v", statErr)
			}
		})
	}
}
