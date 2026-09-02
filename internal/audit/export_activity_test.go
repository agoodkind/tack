// export_activity_test.go covers the descriptor the export activity beacon
// costs, which is one for as long as the beacon is held and none afterwards.
// The export never closes that descriptor itself; it relies on the locking
// library to do so, and these tests are what would notice if a bump of that
// library stopped.

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
)

// beaconContendedDeadline bounds how long a contended export waits for the
// shared beacon before it gives up and runs unmarked. It only needs to outlast
// exportActivityRetry, so the retry path is taken at least once.
const beaconContendedDeadline = 3 * exportActivityRetry

// descriptorGuardExports is how many exports each guard runs between its two
// counts. One leaked descriptor per export is what a regression costs, so the
// count only has to be large enough that the growth cannot be mistaken for
// noise.
const descriptorGuardExports = 16

// openDescriptorCount is how many descriptors this process holds open. The
// kernel lists them under /proc/self/fd on Linux and /dev/fd on macOS, and the
// entries are read by name only, because stat-ing them fails on macOS.
func openDescriptorCount(t *testing.T) int {
	t.Helper()
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		handle, err := os.Open(dir)
		if err != nil {
			continue
		}
		names, err := handle.Readdirnames(-1)
		_ = handle.Close()
		if err != nil {
			t.Fatalf("list descriptors under %s: %v", dir, err)
		}
		return len(names)
	}
	t.Fatal("this platform lists open descriptors under neither /proc/self/fd nor /dev/fd")
	return 0
}

// countDescriptorGrowth runs work between two descriptor counts and returns
// how many more are open afterwards.
//
// Automatic garbage collection is off between the counts. Go closes an
// unreachable *os.File from a finalizer, so a lock handle the library failed to
// close would still be closed by the collector whenever it next ran, and a
// count taken after that would show nothing. With collection off, a descriptor
// that leaked during the work is still open when it is counted.
func countDescriptorGrowth(t *testing.T, work func()) int {
	t.Helper()
	runtime.GC()
	previous := debug.SetGCPercent(-1)
	t.Cleanup(func() { debug.SetGCPercent(previous) })
	before := openDescriptorCount(t)
	work()
	after := openDescriptorCount(t)
	return after - before
}

// descriptorGuardExport runs one export into dir with the row source the guard
// tests share. The context is the caller's, because the contended guard needs
// one that expires.
func descriptorGuardExport(ctx context.Context, t *testing.T, dir string, signer ed25519.PrivateKey) {
	t.Helper()
	orgID := uuid.Must(uuid.NewV7())
	if _, err := Export(ctx, exportTestRows(t, orgID, 5, nil),
		signer, "ed25519:test", exportTestFilter(orgID), dir); err != nil {
		t.Fatalf("export: %v", err)
	}
}

// TestAnExportHoldsNoDescriptorAfterItReturns pins a behaviour of
// github.com/gofrs/flock that the export relies on without ever naming it:
// Unlock closes the lock file's descriptor.
//
// exportActivity.end and the reclaim's deferred release both call Unlock and
// neither calls Close. That is enough in flock v0.13.0, where Close is defined
// as Unlock and Unlock finishes by closing the handle it opened. The dependency
// policy is `go get -u ./...` with freshness winning, so a release that
// separated the two would arrive unattended, and from then on every export
// would hold one descriptor open until the garbage collector happened to
// finalise it. Nothing else in the suite would notice.
func TestAnExportHoldsNoDescriptorAfterItReturns(t *testing.T) {
	dir := t.TempDir()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// The first export into a directory creates the lock file and the manifest;
	// it is run outside the counted window so the counts compare like with like.
	descriptorGuardExport(context.Background(), t, dir, privateKey)

	grew := countDescriptorGrowth(t, func() {
		for range descriptorGuardExports {
			descriptorGuardExport(context.Background(), t, dir, privateKey)
		}
	})
	if grew > 0 {
		t.Fatalf("%d exports left %d descriptors open: the beacon's release no longer closes its handle",
			descriptorGuardExports, grew)
	}
}

// TestAContendedExportHoldsNoDescriptorEither pins the other half of the same
// dependency behaviour: a try for the beacon that fails closes the handle it
// opened. In flock v0.13.0 that is the ensureFhState deferred inside TryRLock
// and TryLock, which closes the file whenever the try ends with no lock held.
//
// This is the branch on which the export itself does nothing. When the shared
// side cannot be taken, exportActivity.end returns without calling into the
// library at all, and the reclaim returns before its deferred release, so
// neither can compensate for a library that kept the handle open. The beacon is
// held exclusively here for the whole window, so every export takes exactly
// that branch on both calls, and the retry loop takes it more than once.
func TestAContendedExportHoldsNoDescriptorEither(t *testing.T) {
	dir := t.TempDir()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	descriptorGuardExport(context.Background(), t, dir, privateKey)

	blocker := flock.New(filepath.Join(dir, exportActivityLockFile))
	held, err := blocker.TryLock()
	if err != nil || !held {
		t.Fatalf("take the beacon exclusively: held=%v err=%v", held, err)
	}
	t.Cleanup(func() { _ = blocker.Unlock() })

	// The beacon has to be genuinely contended, or the exports below take the
	// ordinary path and this test repeats the one above.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), beaconContendedDeadline)
	defer cancelProbe()
	probe := beginExportActivity(probeCtx, dir)
	probe.end()
	if probe.held {
		t.Fatal("an export took the shared beacon while it was held exclusively")
	}

	// An abandoned rows file that a reclaim would remove. It stays because the
	// reclaim, too, cannot take the beacon, which is what shows every export in
	// the window took the contended branch on both calls.
	abandoned := filepath.Join(dir, exportEventsFileName(uuid.Must(uuid.NewV7())))
	if err := os.WriteFile(abandoned, []byte("half a ledger\n"), 0o600); err != nil {
		t.Fatalf("plant the abandoned rows: %v", err)
	}

	grew := countDescriptorGrowth(t, func() {
		for range descriptorGuardExports {
			ctx, cancel := context.WithTimeout(context.Background(), beaconContendedDeadline)
			descriptorGuardExport(ctx, t, dir, privateKey)
			cancel()
		}
	})
	if grew > 0 {
		t.Fatalf("%d contended exports left %d descriptors open: a failed try no longer closes its handle",
			descriptorGuardExports, grew)
	}
	if _, statErr := os.Stat(abandoned); statErr != nil {
		t.Fatalf("a reclaim ran while the beacon was held exclusively: %v", statErr)
	}
}
