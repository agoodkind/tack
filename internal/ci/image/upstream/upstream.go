// Package upstream publishes the pinned upstream Yugabyte image to the workflow output.
package upstream

import (
	"fmt"
	"io"
	"os"

	"goodkind.io/tack/internal/ci/image/compose"
	"goodkind.io/tack/internal/ci/image/environment"
)

const (
	composeFile        = "docker-compose.yml"
	outputFilePermBits = 0o666
)

// Write requires GITHUB_OUTPUT and prints the pinned Yugabyte image from
// docker-compose.yml. After the print succeeds, it appends
// yugabyte_image=<image> to the file specified by GITHUB_OUTPUT.
func Write(stdout io.Writer) error {
	outputPath, err := environment.Required("GITHUB_OUTPUT")
	if err != nil {
		return &os.PathError{Op: "read upstream image output file", Path: "GITHUB_OUTPUT", Err: err}
	}
	image, err := compose.YugabyteImage(composeFile)
	if err != nil {
		return fmt.Errorf("read pinned Yugabyte image: %w", err)
	}
	if _, err := fmt.Fprintf(stdout, "Upstream Yugabyte image is %s\n", image); err != nil {
		return &os.PathError{Op: "write upstream image message", Path: "stdout", Err: err}
	}
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, outputFilePermBits)
	if err != nil {
		return &os.PathError{Op: "open upstream image output file", Path: outputPath, Err: err}
	}
	if _, err := file.WriteString("yugabyte_image=" + image + "\n"); err != nil {
		writeErr := &os.PathError{Op: "write upstream image output file", Path: outputPath, Err: err}
		_ = file.Close()
		return writeErr
	}
	if err := file.Close(); err != nil {
		return &os.PathError{Op: "close upstream image output file", Path: outputPath, Err: err}
	}
	return nil
}
