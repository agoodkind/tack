// Package reference writes image digest and tag files for the build workflow.
package reference

import (
	"log/slog"
	"os"
)

const referenceFilePermBits = 0o666

// writeFile replaces the contents of path and creates the file if it does not exist.
func writeFile(path, content string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, referenceFilePermBits)
	if err != nil {
		return &os.PathError{Op: "open image file", Path: path, Err: err}
	}
	if _, err := file.WriteString(content); err != nil {
		writeErr := &os.PathError{Op: "write image file", Path: path, Err: err}
		if closeErr := file.Close(); closeErr != nil {
			slog.Error("image_file.close_failed", slog.String("path", path), slog.Any("error", closeErr))
		}
		return writeErr
	}
	if err := file.Close(); err != nil {
		return &os.PathError{Op: "close image file", Path: path, Err: err}
	}
	return nil
}
