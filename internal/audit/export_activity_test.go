// export_activity_test.go checks descriptor cleanup after successful exports
// and exports that cannot acquire the activity lock.

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

// beaconContendedDeadline allows at least one exportActivityRetry before
// cancellation of a contended lock attempt.
const beaconContendedDeadline = 3 * exportActivityRetry

// descriptorGuardExports sets the repeat count for detecting descriptor growth.
const descriptorGuardExports = 16

// openDescriptorCount counts descriptor entries under the platform's fd directory.
// Reading names avoids macOS errors from stat calls on those entries.
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

// countDescriptorGrowth compares open descriptors before and after work.
// Disable automatic garbage collection during measurement to prevent os.File
// finalizers from hiding descriptors the lock library failed to close.
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

// descriptorGuardExport runs one export with the supplied context, including
// the expiring context required by the contention test.
func descriptorGuardExport(
	ctx context.Context,
	t *testing.T,
	dir string,
	signer ed25519.PrivateKey,
) {
	t.Helper()
	orgID := uuid.Must(uuid.NewV7())
	if _, err := Export(
		ctx,
		exportTestRows(t, orgID, 5, nil),
		signer,
		"ed25519:test",
		exportTestFilter(orgID),
		dir,
	); err != nil {
		t.Fatalf("export: %v", err)
	}
}

// TestExportClosesLockDescriptors checks that flock.Unlock closes descriptors
// used by activity and reclamation locks without requiring flock.Close.
func TestExportClosesLockDescriptors(t *testing.T) {
	dir := t.TempDir()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	// Create the initial lock and manifest outside the measured window.
	descriptorGuardExport(context.Background(), t, dir, privateKey)

	grew := countDescriptorGrowth(
		t,
		func() {
			for range descriptorGuardExports {
				descriptorGuardExport(context.Background(), t, dir, privateKey)
			}
		},
	)
	if grew > 0 {
		t.Fatalf(
			"%d exports left %d descriptors open: the beacon's release no longer closes its handle",
			descriptorGuardExports,
			grew,
		)
	}
}

// TestExportClosesDescriptorsAfterLockContention checks cleanup after failed
// lock attempts. The export does not call Unlock when acquisition fails;
// the library must close descriptors from its TryRLock and TryLock attempts.
func TestExportClosesDescriptorsAfterLockContention(t *testing.T) {
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

	// Confirm contention before measuring failed acquisition attempts.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), beaconContendedDeadline)
	defer cancelProbe()
	probe := beginExportActivity(probeCtx, dir)
	// Read the acquisition result before end clears it.
	probeHeld := probe.held
	probe.end()
	if probeHeld {
		t.Fatal("an export took the shared beacon while it was held exclusively")
	}

	// The abandoned rows must survive to prove reclamation also failed to
	// acquire the exclusive lock during the measurement.
	abandoned := filepath.Join(dir, exportEventsFileName(uuid.Must(uuid.NewV7())))
	if err := os.WriteFile(abandoned, []byte("half a ledger\n"), 0o600); err != nil {
		t.Fatalf("plant the abandoned rows: %v", err)
	}

	grew := countDescriptorGrowth(
		t,
		func() {
			for range descriptorGuardExports {
				ctx, cancel := context.WithTimeout(context.Background(), beaconContendedDeadline)
				descriptorGuardExport(ctx, t, dir, privateKey)
				cancel()
			}
		},
	)
	if grew > 0 {
		t.Fatalf(
			"%d contended exports left %d descriptors open: a failed try no longer closes its handle",
			descriptorGuardExports,
			grew,
		)
	}

	if _, statErr := os.Stat(abandoned); statErr != nil {
		t.Fatalf("a reclaim ran while the beacon was held exclusively: %v", statErr)
	}
}
