// Package native checks the build runner architecture against the image
// architecture.
package native

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/ci/image/environment"
)

var (
	errUnsupportedArchitecture = errors.New("unsupported image architecture")
	errRunnerMismatch          = errors.New("does not match runner")
)

// Verify checks IMAGE_ARCH against the runner machine type.
//
// It accepts amd64 on x86_64 and arm64 on aarch64. On a match, it writes
// the machine type to stdout. For example:
//
//	Native image runner: x86_64
func Verify(stdout io.Writer) error {
	architecture, err := environment.Required("IMAGE_ARCH")
	if err != nil {
		return &os.PathError{Op: "read native runner architecture", Path: "IMAGE_ARCH", Err: err}
	}

	if architecture != "amd64" && architecture != "arm64" {
		slog.Error(
			"native.architecture_rejected",
			slog.String("architecture", architecture),
			slog.Any("error", errUnsupportedArchitecture),
		)

		return fmt.Errorf("%w: %s", errUnsupportedArchitecture, architecture)
	}

	machine, err := readRunnerMachine()
	if err != nil {
		return err
	}

	if (architecture != "amd64" || machine != "x86_64") &&
		(architecture != "arm64" || machine != "aarch64") {
		return fmt.Errorf("image architecture %s %w %s", architecture, errRunnerMismatch, machine)
	}

	_, err = fmt.Fprintf(stdout, "Native image runner: %s\n", machine)
	if err != nil {
		return &os.PathError{Op: "write native runner output", Path: "stdout", Err: err}
	}

	return nil
}
