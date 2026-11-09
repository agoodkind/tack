package image_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/ci/image/imagetest"
)

const (
	helpFlag         = "--help"
	unknownOperation = "unknown"
)

// TestMain calls RunTests to run the command tests.
//
// RunTests manages the executable for each subprocess in the package:
//
//   - It builds the command once before the tests start.
//   - It removes the build directory after the tests finish.
//   - It returns the exit code that TestMain passes to [os.Exit].
func TestMain(m *testing.M) {
	os.Exit(imagetest.RunTests(m))
}

func runBuiltCommand(
	t *testing.T,
	environment []string,
	build imagetest.Command,
) (string, error) {
	t.Helper()

	command := imagetest.Prepare(t, t.TempDir(), build)
	command.Env = environment
	output, err := command.CombinedOutput()

	return string(output), err
}

// TestHelpSucceedsWithoutOperationEnvironment checks command help.
//
// Each invocation uses an empty environment and must succeed without logging
// ci-image.failed. The cases request help for these commands:
//
//   - The root command supports help.
//   - Each image operation supports help.
func TestHelpSucceedsWithoutOperationEnvironment(t *testing.T) {
	for _, test := range []commandCase{
		{helpFlag, rootHelpCommand},
		{"digest " + helpFlag, digestHelpCommand},
		{"tags " + helpFlag, tagsHelpCommand},
		{"verify-native-runner " + helpFlag, verifyNativeRunnerHelpCommand},
		{"metadata " + helpFlag, metadataHelpCommand},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runBuiltCommand(t, []string{}, test.build)
			if err != nil {
				t.Fatalf("err = %v, output = %s", err, output)
			}

			if strings.Contains(output, "ci-image.failed") {
				t.Fatalf("help logged a failure: %s", output)
			}
		})
	}
}

// TestInvalidArgumentsFailOnceWithoutWritingFiles checks argument rejection.
//
// Each case must fail and log one boundary error. The digest directory must
// remain absent. The cases check these arguments:
//
//   - The caller omits the operation.
//   - The caller requests an unknown operation.
//   - The caller adds an extra argument for digest.
func TestInvalidArgumentsFailOnceWithoutWritingFiles(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "digests")
	environment := []string{
		"IMAGE_ARCH=amd64",
		"IMAGE_DIGEST=sha256:" + strings.Repeat("a", 64),
		"DIGEST_DIRECTORY=" + directory,
	}

	for _, test := range []commandCase{
		{"missing operation", missingOperationCommand},
		{"unknown operation", unknownOperationCommand},
		{"extra argument", extraArgumentCommand},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := runBuiltCommand(t, environment, test.build)
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
