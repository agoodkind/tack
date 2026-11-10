// Export durability forms part of the compliance contract. Publication and
// reclamation must follow persistence of the files and directory entries they
// depend on.

package audit

import (
	"fmt"
	"log/slog"
	"os"
)

// syncExportDirectory persists a bundle directory's entries to stable storage.
//
// A file sync persists contents without persisting its directory entry. Creates
// and renames modify that directory. Power loss after a file sync but before a
// directory sync can leave bytes without a filename, equivalent to missing
// rows. A read-only directory descriptor suffices for fsync.
//
// Any open, sync, or close error prevents dependent operations from proceeding.
// Errors share directory-sync context and wrap their specific cause. Distinct
// log events distinguish an open failure from a filesystem sync failure.
func syncExportDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		slog.Error(
			"audit.export.dir_open_failed",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit export sync dir %s: %w", dir, err)
	}

	if syncErr := handle.Sync(); syncErr != nil {
		_ = handle.Close()
		slog.Error(
			"audit.export.dir_sync_failed",
			slog.String("dir", dir),
			slog.String("err", syncErr.Error()),
		)

		return fmt.Errorf("audit export sync dir %s: %w", dir, syncErr)
	}

	if closeErr := handle.Close(); closeErr != nil {
		slog.Error(
			"audit.export.dir_close_failed",
			slog.String("dir", dir),
			slog.String("err", closeErr.Error()),
		)

		return fmt.Errorf("audit export sync dir %s: %w", dir, closeErr)
	}

	return nil
}

// writeSyncedFile writes file contents to stable storage before a successful
// return.
//
// A rename publishes a directory entry without persisting contents. Renaming an
// unsynced staged manifest can leave an empty or incomplete published
// compliance artifact after power loss.
func writeSyncedFile(path string, body []byte, perm os.FileMode) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		slog.Error(
			"audit.export.staged_open_failed",
			slog.String("path", path),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit export open %s: %w", path, err)
	}

	if _, writeErr := handle.Write(body); writeErr != nil {
		_ = handle.Close()
		slog.Error(
			"audit.export.staged_write_failed",
			slog.String("path", path),
			slog.String("err", writeErr.Error()),
		)

		return fmt.Errorf("audit export write %s: %w", path, writeErr)
	}

	if syncErr := handle.Sync(); syncErr != nil {
		_ = handle.Close()
		slog.Error(
			"audit.export.staged_sync_failed",
			slog.String("path", path),
			slog.String("err", syncErr.Error()),
		)

		return fmt.Errorf("audit export sync %s: %w", path, syncErr)
	}

	if closeErr := handle.Close(); closeErr != nil {
		slog.Error(
			"audit.export.staged_close_failed",
			slog.String("path", path),
			slog.String("err", closeErr.Error()),
		)

		return fmt.Errorf("audit export close %s: %w", path, closeErr)
	}

	return nil
}
