// export_durable.go forces an export's writes to stable storage. A bundle is a
// compliance artifact, so the order in which its parts become durable is part of
// the contract rather than a tuning choice: every step that publishes a manifest
// or frees the rows it replaces runs after the thing it depends on is on disk.

package audit

import (
	"fmt"
	"log/slog"
	"os"
)

// syncExportDirectory forces a bundle directory's own entries to stable storage.
//
// Syncing a file persists that file's contents. It says nothing about the
// directory entry naming it, and a create or a rename is a change to the
// directory, not to the file. A host that lost power after the rows were synced
// but before the directory was could come back with the bytes on disk and no
// name pointing at them, which is indistinguishable from rows that were never
// written at all.
//
// The directory is opened read-only, which is all fsync needs.
//
// Every way this can fail means one thing to the caller: the directory was not
// made durable, so the steps that depend on it must not run. They therefore
// share one error, and the cause each one wraps says which part gave way. The
// log events stay distinct so an operator can tell an unopenable directory from
// a filesystem that refused the sync.
func syncExportDirectory(dir string) error {
	handle, err := os.Open(dir)
	if err != nil {
		slog.Error("audit.export.dir_open_failed",
			slog.String("dir", dir), slog.String("err", err.Error()))
		return fmt.Errorf("audit export sync dir %s: %w", dir, err)
	}
	if syncErr := handle.Sync(); syncErr != nil {
		_ = handle.Close()
		slog.Error("audit.export.dir_sync_failed",
			slog.String("dir", dir), slog.String("err", syncErr.Error()))
		return fmt.Errorf("audit export sync dir %s: %w", dir, syncErr)
	}
	if closeErr := handle.Close(); closeErr != nil {
		slog.Error("audit.export.dir_close_failed",
			slog.String("dir", dir), slog.String("err", closeErr.Error()))
		return fmt.Errorf("audit export sync dir %s: %w", dir, closeErr)
	}
	return nil
}

// writeSyncedFile writes a file whose contents are on stable storage before it
// returns.
//
// A rename publishes a name, not the bytes behind it. Staging a manifest with an
// ordinary write and renaming it over the published name can leave a host that
// lost power holding the new name over contents that never reached the disk,
// which is a published compliance artifact that is empty or torn.
func writeSyncedFile(path string, body []byte, perm os.FileMode) error {
	handle, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		slog.Error("audit.export.staged_open_failed",
			slog.String("path", path), slog.String("err", err.Error()))
		return fmt.Errorf("audit export open %s: %w", path, err)
	}
	if _, writeErr := handle.Write(body); writeErr != nil {
		_ = handle.Close()
		slog.Error("audit.export.staged_write_failed",
			slog.String("path", path), slog.String("err", writeErr.Error()))
		return fmt.Errorf("audit export write %s: %w", path, writeErr)
	}
	if syncErr := handle.Sync(); syncErr != nil {
		_ = handle.Close()
		slog.Error("audit.export.staged_sync_failed",
			slog.String("path", path), slog.String("err", syncErr.Error()))
		return fmt.Errorf("audit export sync %s: %w", path, syncErr)
	}
	if closeErr := handle.Close(); closeErr != nil {
		slog.Error("audit.export.staged_close_failed",
			slog.String("path", path), slog.String("err", closeErr.Error()))
		return fmt.Errorf("audit export close %s: %w", path, closeErr)
	}
	return nil
}
