// Export activity uses one file descriptor while its lock remains active and
// does not retain a descriptor afterward. The export relies on the locking
// library to close descriptors; these tests check that dependency behavior.

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

// beaconContendedDeadline bounds the wait for a contended shared activity lock.
// The export proceeds without the lock after the deadline. The deadline exceeds
// exportActivityRetry to exercise at least one retry.
const beaconContendedDeadline = 3 * exportActivityRetry

// descriptorGuardExports sets the number of exports between descriptor counts.
// A regression leaks one descriptor per export; the count distinguishes that
// growth from measurement noise.
const descriptorGuardExports = 16

// openDescriptorCount counts this process's open descriptors. Linux lists them
// under /proc/self/fd, and macOS uses /dev/fd. The function reads entry names
// because stat fails for these entries on macOS.
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

// countDescriptorGrowth runs work between descriptor counts and returns the
// increase.
//
// The function disables automatic garbage collection between counts. A
// finalizer can close an unreachable *os.File and hide a leaked lock
// descriptor. Disabling collection preserves leaked descriptors until the
// second count.
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

// descriptorGuardExport runs one export with the shared test row source. The
// caller supplies the context because the contention test needs a deadline.
func descriptorGuardExport(ctx context.Context, t *testing.T, dir string, signer ed25519.PrivateKey) {
	t.Helper()
	orgID := uuid.Must(uuid.NewV7())
	if _, err := Export(ctx, exportTestRows(t, orgID, 5, nil),
		signer, "ed25519:test", exportTestFilter(orgID), dir); err != nil {
		t.Fatalf("export: %v", err)
	}
}

// TestAnExportHoldsNoDescriptorAfterItReturns checks that releasing an export
// activity lock closes its file descriptor.
//
// Both exportActivity.end and the reclaim's deferred release use Unlock without
// Close. In flock v0.13.0, Close delegates to Unlock, and Unlock closes the
// handle. The guard records a dependency policy of go get -u ./... with
// freshness as the priority. A release separating those operations could retain
// a descriptor after each export until garbage collection finalizes it. This
// test specifically checks that dependency behavior. The current flock v0.13.1
// retains the same descriptor closure.
func TestAnExportHoldsNoDescriptorAfterItReturns(t *testing.T) {
	dir := t.TempDir()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// The first export initializes the lock file and manifest outside the
	// measurement interval. Both measurements use that initialized directory.
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

// TestAContendedExportHoldsNoDescriptorEither checks descriptor closure after
// failed lock attempts. In flock v0.13.0, TryRLock and TryLock defer
// ensureFhState, which closes the file when an attempt ends without a lock.
//
// When the export cannot acquire the shared lock, exportActivity.end returns
// without a library call. Reclaim also returns before its deferred release.
// Neither operation can close a descriptor that the failed attempt retained.
// The test acquires an exclusive lock for the full interval, forcing both
// branches and repeated attempts in the retry loop.
//
// The current flock v0.13.1 also closes descriptors after failed attempts.
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

	// The probe verifies contention. Without the exclusive lock, these exports
	// would repeat the ordinary-path test.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), beaconContendedDeadline)
	defer cancelProbe()
	probe := beginExportActivity(probeCtx, dir)
	// Read held before end clears the flag during release.
	probeHeld := probe.held
	probe.end()
	if probeHeld {
		t.Fatal("an export took the shared beacon while it was held exclusively")
	}

	// The abandoned rows file qualifies for reclaim, but the exclusive lock
	// prevents reclamation. Its survival checks that every measured export used the
	// contended branch for both lock operations.
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
