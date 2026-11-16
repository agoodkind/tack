// export_open.go checks the file type before reading bundle rows. The verifier
// must read rows before reporting a signature verdict and treats paths as untrusted.

package audit

import (
	"fmt"
	"log/slog"
	"os"
	"syscall"
)

// openBundleRowsFile opens only regular files. O_NOFOLLOW rejects a final
// symbolic link with ELOOP instead of reading its target. O_NONBLOCK opens
// FIFO (first-in, first-out) special files without waiting for a writer; the
// descriptor check then rejects them. Neither flag changes regular file reads.
// Checking the opened descriptor prevents a path replacement between a type
// check and an open.
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
