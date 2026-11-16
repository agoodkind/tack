// export_reclaim.go removes superseded and interrupted export files that the
// published manifest does not reference.

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

// exportActivityLockFile coordinates active writers and reclamation. Writers
// acquire shared locks; reclamation requires an exclusive lock before deleting
// unreferenced files. The kernel releases locks after process exit, allowing
// reclamation of interrupted exports without an age cutoff.
//
// Readers do not acquire this lock. Offline verification must work on read-only
// copies, and a long verification must not block reclamation indefinitely.
// Reclamation can remove rows between the manifest read and the rows open;
// verification then fails explicitly. An open descriptor remains readable after
// unlink. The verifier must not silently retry against a different export ID.
const exportActivityLockFile = ".export.lock"

// exportActivityRetry sets the retry interval while reclamation owns the
// exclusive lock for directory listing and deletion.
const exportActivityRetry = 5 * time.Millisecond

// exportActivity records a shared lock for a writer. Failure to acquire it does
// not fail the export; reclamation also requires successful lock acquisition.
type exportActivity struct {
	lock *flock.Flock
	held bool
}

// beginExportActivity acquires a shared activity lock for dir.
func beginExportActivity(ctx context.Context, dir string) *exportActivity {
	lock := flock.New(filepath.Join(dir, exportActivityLockFile))
	held, err := lock.TryRLockContext(ctx, exportActivityRetry)
	if err != nil {
		slog.DebugContext(
			ctx,
			"audit.export.activity_unavailable",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)
	}

	return &exportActivity{lock: lock, held: held}
}

// end releases the shared lock and supports repeated calls before reclamation
// and during deferred cleanup.
func (a *exportActivity) end() {
	if !a.held {
		return
	}

	a.held = false
	_ = a.lock.Unlock()
}

// reclaimAbandonedExportFiles removes unreferenced exporter files under an
// exclusive lock. Failures log an error without failing an already-published
// compliance export.
func reclaimAbandonedExportFiles(ctx context.Context, dir string) {
	lock := flock.New(filepath.Join(dir, exportActivityLockFile))
	held, err := lock.TryLock()
	if err != nil || !held {
		// Defer reclamation while a writer is active or locking is unavailable.
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
		slog.ErrorContext(
			ctx,
			"audit.export.reclaim_failed",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return
	}

	removed := 0

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || name == keep || !exporterOwnedFile(name) {
			continue
		}

		if err := os.Remove(filepath.Join(dir, name)); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.export.reclaim_failed",
				slog.String("path", filepath.Join(dir, name)),
				slog.String("err", err.Error()),
			)
			continue
		}

		removed++
	}

	if removed > 0 {
		slog.InfoContext(
			ctx,
			"audit.export.reclaimed",
			slog.String("dir", dir),
			slog.Int("files", removed),
		)
	}
}

// publishedEventsFileName returns the referenced rows file and permission to
// reclaim. Without a manifest, all exporter files are reclaimable. An unreadable
// or invalid existing manifest stops reclamation to preserve rows for repair.
func publishedEventsFileName(ctx context.Context, dir string) (string, bool) {
	// Check absence before reading to avoid logging an expected missing
	// manifest as an error during the first export.
	if _, err := os.Stat(filepath.Join(dir, exportManifestFile)); errors.Is(err, fs.ErrNotExist) {
		return "", true
	}

	manifest, err := readExportManifest(dir)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.export.reclaim_skipped",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return "", false
	}

	name, err := bundleEventsFileName(manifest)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.export.reclaim_skipped",
			slog.String("dir", dir),
			slog.String("err", err.Error()),
		)

		return "", false
	}

	return name, true
}
