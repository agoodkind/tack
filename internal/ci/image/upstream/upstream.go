// Package upstream publishes the pinned upstream Yugabyte image to the
// workflow output.
package upstream

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/tack/internal/ci/image/compose"
	"goodkind.io/tack/internal/ci/image/environment"
)

const (
	composeFile        = "docker-compose.yml"
	outputFilePermBits = 0o666
)

// Write prints the pinned Yugabyte image and appends it to the workflow
// output file.
//
// Write reads the image from docker-compose.yml and requires GITHUB_OUTPUT
// to identify the output file. It appends the output only after the print
// succeeds.
//
// For the image yugabytedb/yugabyte:2024.2.8.0-b85, Write appends:
//
//	yugabyte_image=yugabytedb/yugabyte:2024.2.8.0-b85
func Write(stdout io.Writer) error {
	outputPath, err := environment.Required("GITHUB_OUTPUT")
	if err != nil {
		return &os.PathError{Op: "read output environment variable", Path: "GITHUB_OUTPUT", Err: err}
	}

	image, err := compose.YugabyteImage(composeFile)
	if err != nil {
		slog.Error(
			"upstream.compose_image_failed",
			slog.String("path", composeFile),
			slog.Any("error", err),
		)

		return fmt.Errorf("read pinned Yugabyte image from %s: %w", composeFile, err)
	}

	_, err = fmt.Fprintf(stdout, "Upstream Yugabyte image is %s\n", image)
	if err != nil {
		return &os.PathError{Op: "write upstream image message", Path: "stdout", Err: err}
	}

	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, outputFilePermBits)
	if err != nil {
		return &os.PathError{Op: "open upstream image output file", Path: outputPath, Err: err}
	}

	_, err = file.WriteString("yugabyte_image=" + image + "\n")
	if err != nil {
		writeErr := &os.PathError{Op: "write upstream image output file", Path: outputPath, Err: err}
		_ = file.Close()

		return writeErr
	}

	err = file.Close()
	if err != nil {
		return &os.PathError{Op: "close upstream image output file", Path: outputPath, Err: err}
	}

	return nil
}
