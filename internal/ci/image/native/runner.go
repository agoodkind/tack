// Package native checks the build runner architecture against the image architecture.
package native

import (
	"fmt"
	"io"
	"os"

	"goodkind.io/tack/internal/ci/image/environment"
)

// Verify compares IMAGE_ARCH with the runner machine type and writes the machine type to stdout when they match.
func Verify(stdout io.Writer) error {
	architecture, err := environment.Required("IMAGE_ARCH")
	if err != nil {
		return &os.PathError{Op: "read native runner architecture", Path: "IMAGE_ARCH", Err: err}
	}
	if architecture != "amd64" && architecture != "arm64" {
		return fmt.Errorf("unsupported image architecture: %s", architecture)
	}
	machine, err := readRunnerMachine()
	if err != nil {
		return err
	}
	if (architecture != "amd64" || machine != "x86_64") && (architecture != "arm64" || machine != "aarch64") {
		return fmt.Errorf("image architecture %s does not match runner %s", architecture, machine)
	}
	if _, err := fmt.Fprintf(stdout, "Native image runner: %s\n", machine); err != nil {
		return &os.PathError{Op: "write native runner output", Path: "stdout", Err: err}
	}
	return nil
}
