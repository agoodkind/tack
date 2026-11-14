// export_durable.go synchronizes export writes before publication or removal
// of superseded rows. Each dependent step requires successful synchronization.

package audit

import (
	"fmt"
	"log/slog"
	"os"
)

// syncExportDirectory synchronizes directory entries to stable storage.
// File synchronization persists contents but does not persist creates or
// renames in the directory. Power loss between file and directory sync can
// leave durable bytes without their file name. Open, sync, and close failures
// return the same contextual error with distinct log events. Callers must
// stop publication or reclamation on any failure.
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

// writeSyncedFile writes and synchronizes file contents before returning.
// A rename alone does not persist contents. Publishing an unsynchronized
// manifest can leave an empty or partial compliance artifact after power loss.
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
