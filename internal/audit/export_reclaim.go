// Export cleanup removes superseded rows and files that interrupted exports
// abandoned. A bundle consists of a manifest and its selected rows file.
// Cleanup can remove other exporter-owned files after excluding active exports.

package audit

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

// exportActivityLockFile identifies the file that coordinates active exports
// with cleanup. An export acquires a shared lock while writing into a bundle
// directory. Cleanup requires an exclusive lock.
//
// A manifest can omit both abandoned rows and rows that an active export still
// writes. Deleting active rows would prevent publication. File contents and age
// cannot establish that the writer has stopped. The exclusive lock excludes
// active writers, and the kernel releases a dead process's shared lock. Cleanup
// can then reclaim its abandoned rows without an age cutoff.
//
// Shared locks admit concurrent exports and do not serialize their
// publications. They indicate that exports remain active.
//
// [VerifyBundle] does not acquire this lock. Offline verification must work on
// another host, on copies from object storage, on read-only media, and with an
// auditor's read-only access. Acquiring the lock requires creating and locking
// a file in the bundle directory. Requiring that write would prevent those
// checks; silently omitting a required lock would not provide a guarantee.
//
// Cleanup can delete selected rows between the verifier's manifest read and its
// rows-file open. The open then fails. After a successful open, the file
// descriptor permits the verifier to finish reading even if cleanup unlinks the
// file. The verifier reports on the file it opened or reports the open failure.
//
// A verifier's shared lock would not delay export writes, but it would delay
// cleanup's exclusive lock. Verification of a ledger-sized bundle can take
// minutes. Overlapping scheduled verifications could prevent cleanup
// indefinitely. Cleanup alone removes superseded and abandoned rows files, and
// each file can occupy the size of a ledger export.
//
// The report identifies its export ID. An internal retry that rereads a
// replacement manifest could verify a different export. The verifier returns
// the open failure, and the operator can run verification again for an explicit
// new report.
const exportActivityLockFile = ".export.lock"

// exportActivityRetry sets the interval between shared-lock attempts while
// cleanup owns the exclusive lock. Cleanup lists the directory and removes
// eligible files; it does not wait for a full export.
const exportActivityRetry = 5 * time.Millisecond

// exportActivity records whether an export acquired the directory's shared
// activity lock. The export can continue after a lock error. Cleanup skips
// deletion if it cannot acquire its exclusive lock.
type exportActivity struct {
	lock *flock.Flock
	held bool
}

// beginExportActivity attempts to acquire a shared activity lock for the bundle
// directory.
func beginExportActivity(ctx context.Context, dir string) *exportActivity {
	lock := flock.New(filepath.Join(dir, exportActivityLockFile))
	held, err := lock.TryRLockContext(ctx, exportActivityRetry)
	if err != nil {
		slog.DebugContext(ctx, "audit.export.activity_unavailable",
			slog.String("dir", dir), slog.String("err", err.Error()))
	}
	return &exportActivity{lock: lock, held: held}
}

// end releases the shared activity lock once. The export invokes end before
// cleanup and again when it returns.
func (a *exportActivity) end() {
	if !a.held {
		return
	}
	a.held = false
	_ = a.lock.Unlock()
}

// reclaimAbandonedExportFiles removes exporter-owned files from dir when the
// published manifest does not select them and the activity lock permits
// cleanup.
//
// Cleanup logs failures without returning them to the export caller.
// Publication has already completed; a failure to delete older rows must not
// invalidate that completed compliance export.
func reclaimAbandonedExportFiles(ctx context.Context, dir string) {
	lock := flock.New(filepath.Join(dir, exportActivityLockFile))
	held, err := lock.TryLock()
	if err != nil || !held {
		// Cleanup cannot establish exclusive access while another export writes or lock
		// acquisition fails. A completing export attempts cleanup again.
		slog.DebugContext(ctx, "audit.export.reclaim_deferred", slog.String("dir", dir))
		return
	}
	defer func() { _ = lock.Unlock() }()

	keep, ok := publishedEventsFileName(ctx, dir)
	if !ok {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.ErrorContext(ctx, "audit.export.reclaim_failed",
			slog.String("dir", dir), slog.String("err", err.Error()))
		return
	}
	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == keep || !exporterOwnedFile(name) {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			slog.ErrorContext(ctx, "audit.export.reclaim_failed",
				slog.String("path", filepath.Join(dir, name)), slog.String("err", err.Error()))
			continue
		}
		removed++
	}
	if removed > 0 {
		slog.InfoContext(ctx, "audit.export.reclaimed",
			slog.String("dir", dir), slog.Int("files", removed))
	}
}

// publishedEventsFileName returns the rows filename from the published manifest
// and whether cleanup may proceed.
//
// A directory without a manifest lacks a published bundle, and its
// exporter-owned files qualify for cleanup. If reading the manifest or
// resolving its rows filename fails, cleanup preserves the files. An operator
// may need those rows to repair the manifest.
func publishedEventsFileName(ctx context.Context, dir string) (string, bool) {
	// Check for an absent manifest before reading it. The first export commonly
	// starts in an empty directory, and the manifest reader logs a missing file as
	// an error.
	if _, err := os.Stat(filepath.Join(dir, exportManifestFile)); errors.Is(err, fs.ErrNotExist) {
		return "", true
	}
	manifest, err := readExportManifest(dir)
	if err != nil {
		slog.ErrorContext(ctx, "audit.export.reclaim_skipped",
			slog.String("dir", dir), slog.String("err", err.Error()))
		return "", false
	}
	name, err := bundleEventsFileName(manifest)
	if err != nil {
		slog.ErrorContext(ctx, "audit.export.reclaim_skipped",
			slog.String("dir", dir), slog.String("err", err.Error()))
		return "", false
	}
	return name, true
}
