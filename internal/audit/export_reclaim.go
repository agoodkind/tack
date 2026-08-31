// export_reclaim.go frees the rows of exports that were superseded or that died
// before they could publish. A bundle is a manifest and the rows it names, so
// the rule is exact: anything else this exporter wrote is reclaimable.

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

// exportActivityLockFile is the beacon an export holds, shared, for as long as
// it is writing into a bundle directory, and that the reclaim takes
// exclusively.
//
// The reclaim needs it because "no manifest names this file" is true of two
// different files: the rows of an export that is finished, and the rows of an
// export that is still streaming into them. Deleting the second breaks a bundle
// that was about to be published, and no property of the file itself separates
// the two. Holding an exclusive lock is what separates them, because it can
// only be taken when no export is writing, and the kernel drops the shared side
// when a process dies, so the rows a killed export abandoned become reclaimable
// the moment it stops running. That is the same rule an age cutoff only
// guesses at.
//
// Nothing about publishing depends on this. Exports never contend for it, since
// a shared lock admits any number of them; it says only that one is running.
const exportActivityLockFile = ".export.lock"

// exportActivityRetry is how often an export retries the shared beacon while a
// reclaim holds it exclusively. A reclaim is a directory listing and a few
// unlinks, so the wait is bounded by that rather than by any export's length.
const exportActivityRetry = 5 * time.Millisecond

// exportActivity is one export's declaration that it is writing into a bundle
// directory. A failure to take the beacon is not a failure of the export: it
// means this filesystem cannot report activity, in which case no reclaim can
// take the exclusive side either and nothing is removed anywhere.
type exportActivity struct {
	lock *flock.Flock
	held bool
}

// beginExportActivity marks the directory as having an export in flight.
func beginExportActivity(ctx context.Context, dir string) *exportActivity {
	lock := flock.New(filepath.Join(dir, exportActivityLockFile))
	held, err := lock.TryRLockContext(ctx, exportActivityRetry)
	if err != nil {
		slog.DebugContext(ctx, "audit.export.activity_unavailable",
			slog.String("dir", dir), slog.String("err", err.Error()))
	}
	return &exportActivity{lock: lock, held: held}
}

// end releases the beacon. It is safe to call more than once, because the
// export releases it before reclaiming and again on the way out.
func (a *exportActivity) end() {
	if !a.held {
		return
	}
	a.held = false
	_ = a.lock.Unlock()
}

// reclaimAbandonedExportFiles removes every file this exporter wrote into dir
// that the published manifest does not name.
//
// It is best effort by design and reports through the log rather than the
// caller: the bundle this export published is already complete and correct, and
// failing an export because stale rows from an earlier one could not be freed
// would turn a housekeeping problem into a lost compliance export.
func reclaimAbandonedExportFiles(ctx context.Context, dir string) {
	lock := flock.New(filepath.Join(dir, exportActivityLockFile))
	held, err := lock.TryLock()
	if err != nil || !held {
		// Another export is writing, so its rows are not abandoned and this
		// directory is not settled. That export reclaims when it finishes.
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

// publishedEventsFileName is the rows file the directory's published manifest
// names, and whether the reclaim may run at all.
//
// A directory with no manifest holds no bundle, so nothing in it is named and
// everything this exporter wrote there is reclaimable. A manifest that is
// present but cannot be read or resolved is the opposite case: it may still be
// repairable by hand, and the rows it describes are the evidence that would
// make that repair worth anything, so nothing is removed.
func publishedEventsFileName(ctx context.Context, dir string) (string, bool) {
	// The absence is checked before the read rather than recovered from it,
	// because a first export into an empty directory is the ordinary case and
	// the manifest read reports a miss as a failure.
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
