// Package upstream publishes the pinned upstream Yugabyte image to the
// workflow output.
package upstream

import (
	"fmt"
	"io"
	"os"

	"goodkind.io/tack/internal/ci/image/environment"
)

// YugabyteImage is the pinned upstream Yugabyte image.
//
// The upstream-yugabyte command prints YugabyteImage, and the overlay drift
// check reads it. docker-compose.yml declares the same image. Keep both
// values equal when changing either one.
//
//	YugabyteImage = "yugabytedb/yugabyte:2024.2.8.0-b85"
const YugabyteImage = "yugabytedb/yugabyte:2024.2.8.0-b85"

const outputFilePermBits = 0o666

// Write prints the pinned Yugabyte image and appends it to the workflow
// output file.
//
// Write uses YugabyteImage and requires GITHUB_OUTPUT to identify the
// output file. It appends the output only after the print succeeds.
//
// For the image yugabytedb/yugabyte:2024.2.8.0-b85, Write appends:
//
//	yugabyte_image=yugabytedb/yugabyte:2024.2.8.0-b85
func Write(stdout io.Writer) error {
	outputPath, err := environment.Required("GITHUB_OUTPUT")
	if err != nil {
		return &os.PathError{Op: "read output environment variable", Path: "GITHUB_OUTPUT", Err: err}
	}

	_, err = fmt.Fprintf(stdout, "Upstream Yugabyte image is %s\n", YugabyteImage)
	if err != nil {
		return &os.PathError{Op: "write upstream image message", Path: "stdout", Err: err}
	}

	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, outputFilePermBits)
	if err != nil {
		return &os.PathError{Op: "open upstream image output file", Path: outputPath, Err: err}
	}

	_, err = file.WriteString("yugabyte_image=" + YugabyteImage + "\n")
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
