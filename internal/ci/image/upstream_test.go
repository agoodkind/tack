package image_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const imagePrinted = "Upstream Yugabyte image is"

func upstreamYugabyteCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "./ci-image", "upstream-yugabyte")
}

func runUpstream(t *testing.T, directory string, environment ...string) (string, error) {
	t.Helper()

	command := imagetest.Prepare(t, directory, upstreamYugabyteCommand)
	// A nil Env inherits the parent environment, including GITHUB_OUTPUT on CI.
	// A non-nil Env uses the listed variables, even when the slice is empty.
	command.Env = append([]string{}, environment...)
	output, err := command.CombinedOutput()

	return string(output), err
}

// TestUpstreamYugabyteAppendsPinnedImage checks the output and append.
//
// The test runs upstream-yugabyte in a temporary directory without a
// Compose file. It checks that the command prints the pinned image and
// appends yugabyte_image=<image> to GITHUB_OUTPUT after existing content.
func TestUpstreamYugabyteAppendsPinnedImage(t *testing.T) {
	directory := t.TempDir()
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

	if string(content) != "existing=value\nyugabyte_image="+pinnedYugabyteImage+"\n" {
		t.Fatalf("output file = %q", content)
	}

	if !strings.Contains(output, pinnedYugabyteImage) {
		t.Fatalf("console output = %s", output)
	}
}

// TestUpstreamYugabyteRefusesMissingOutputWithoutAppending checks refusal
// when the command environment omits GITHUB_OUTPUT.
//
// The test sets GITHUB_OUTPUT in the test process and omits it from the
// command environment. It checks that the command fails with one
// ci-image.failed event reporting the unset variable and does not print
// the image. It also checks that the command creates neither the local
// output file nor the file identified by the test process variable.
func TestUpstreamYugabyteRefusesMissingOutputWithoutAppending(t *testing.T) {
	directory := t.TempDir()
	outputFile := filepath.Join(directory, "github-output")
	parentOutput := filepath.Join(t.TempDir(), "parent-github-output")

	t.Setenv("GITHUB_OUTPUT", parentOutput)

	output, err := runUpstream(t, directory)
	if err == nil {
		t.Fatalf("expected failure, output = %s", output)
	}

	if strings.Count(output, "ci-image.failed") != 1 {
		t.Fatalf("expected one ci-image.failed boundary event, output = %s", output)
	}

	wantCause := "GITHUB_OUTPUT: required environment variable is unset"
	if !strings.Contains(output, wantCause) {
		t.Fatalf("output does not contain the cause %q: %s", wantCause, output)
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
}
