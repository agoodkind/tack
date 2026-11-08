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
	helpFlag         = "--help"
	unknownOperation = "unknown"
)

// TestMain builds the image command once and runs the package tests against it.
func TestMain(m *testing.M) {
	os.Exit(imagetest.RunTests(m))
}

func runWithArguments(t *testing.T, environment []string, arguments ...string) (string, error) {
	t.Helper()

	command := exec.CommandContext(t.Context(), imagetest.Path(), arguments...)
	command.Env = environment
	output, err := command.CombinedOutput()

	return string(output), err
}

// TestHelpSucceedsWithoutOperationEnvironment checks that help succeeds
// without operation environment variables or a failure log.
func TestHelpSucceedsWithoutOperationEnvironment(t *testing.T) {
	for _, arguments := range [][]string{
		{helpFlag},
		{"digest", helpFlag},
		{"tags", helpFlag},
		{"verify-native-runner", helpFlag},
		{"metadata", helpFlag},
	} {
		t.Run(strings.Join(arguments, " "), func(t *testing.T) {
			output, err := runWithArguments(t, []string{}, arguments...)
			if err != nil {
				t.Fatalf("err = %v, output = %s", err, output)
			}

			if strings.Contains(output, "ci-image.failed") {
				t.Fatalf("help logged a failure: %s", output)
			}
		})
	}
}

// TestInvalidArgumentsFailOnceWithoutWritingFiles checks that invalid
// arguments produce one failure log and do not create a digest directory.
func TestInvalidArgumentsFailOnceWithoutWritingFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "digests")
	environment := []string{
		"IMAGE_ARCH=amd64",
		"IMAGE_DIGEST=sha256:" + strings.Repeat("a", 64),
		"DIGEST_DIRECTORY=" + directory,
	}

	for _, test := range []struct {
		name      string
		arguments []string
	}{
		{"missing operation", nil},
		{"unknown operation", []string{unknownOperation}},
		{"extra argument", []string{"digest", "extra"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runWithArguments(t, environment, test.arguments...)
			if err == nil {
				t.Fatalf("expected failure, output = %s", output)
			}

			if strings.Count(output, "ERROR ") != 1 || !strings.Contains(output, "ci-image.failed") {
				t.Fatalf("expected one boundary log, output = %s", output)
			}

			_, statErr := os.Stat(directory)
			if !os.IsNotExist(statErr) {
				t.Fatalf("digest directory exists after a rejected command: %v", statErr)
			}
		})
	}
}
