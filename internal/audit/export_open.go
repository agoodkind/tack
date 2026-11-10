// The verifier reads the manifest-selected rows even when the manifest
// signature fails. It computes row and file digests before returning the
// complete report. Open-time checks determine the actual entry type.

package audit

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// openBundleRowsFile opens bundle rows and accepts only a regular file.
//
// A plain filename can identify a symbolic link or named pipe. A symbolic link
// could select an attacker's file for digest and row-hash computation. A named
// pipe without a writer could block open(2) indefinitely without a verdict.
//
// O_NOFOLLOW rejects a symbolic link in the final component with ELOOP before
// reading its target. O_NONBLOCK opens a pipe immediately for rejection by the
// regular-file check. Neither flag changes regular-file reads.
//
// The function checks the opened descriptor rather than checking the path
// before opening it. Replacing a path between a separate check and open cannot
// bypass the descriptor's type check.
func openBundleRowsFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		slog.Error(
			"audit.verify.rows_open_refused",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("open bundle rows %s: %w", path, err)
	}

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		slog.Error(
			"audit.verify.rows_stat_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("stat bundle rows %s: %w", path, err)
	}

	if !info.Mode().IsRegular() {
		_ = file.Close()
		kindErr := fmt.Errorf("bundle rows %s is not a regular file: mode %s", path, info.Mode())
		slog.Error(
			"audit.verify.rows_not_regular",
			slog.String("path", path),
			slog.String("err", kindErr.Error()),
		)

		return nil, kindErr
	}

	return file, nil
}
