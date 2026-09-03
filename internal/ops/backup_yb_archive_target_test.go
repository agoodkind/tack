package ops

import (
	"context"
	"testing"
)

// TestResolveYBArchiveTargetRedoesAnArchiveWithoutItsInventory is the defect
// this file exists for. The archive command decided it was done by probing the
// archive object alone, so an archive uploaded before inventories existed read
// as finished and no inventory was ever written for it; the restore drill then
// refused the run for lacking one, and nothing healed it. Against a real store
// over HTTP, yb1's prefix holds the archive and no inventory and must come back
// as work to do, while yb2, which holds both, has nothing to do.
func TestResolveYBArchiveTargetRedoesAnArchiveWithoutItsInventory(t *testing.T) {
	ctx := context.Background()
	const runID = "20260830T010203Z"
	manifest := newYBSnapshotManifest(runID, "snap-1", "tack", []string{"yb1", "yb2"}, ybTestArtifactNames())
	yb1 := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	objects := fakeYBExportRunObjects(t, runID, manifest)
	delete(objects, ybNodeInventoryKey(runID, yb1))
	s3Client, cfg := newFakeBackupObjectStore(t, "tack-backups", objects)

	target, err := resolveYBArchiveTarget(ctx, cfg, s3Client, "yb1", "")
	if err != nil {
		t.Fatalf("resolve the target for yb1: %v", err)
	}
	if target == nil {
		t.Fatal("an archive in the store without its inventory must be redone, but yb1 found nothing to do")
	}
	if target.manifest.RunID != runID || target.prefix != ybNodeKeyPrefix(runID, yb1) {
		t.Fatalf("yb1 resolved run %q prefix %q, want run %q prefix %q",
			target.manifest.RunID, target.prefix, runID, ybNodeKeyPrefix(runID, yb1))
	}

	done, err := resolveYBArchiveTarget(ctx, cfg, s3Client, "yb2", "")
	if err != nil {
		t.Fatalf("resolve the target for yb2: %v", err)
	}
	if done != nil {
		t.Fatalf("a node whose every artifact is in the store must have nothing to do, got prefix %q", done.prefix)
	}
}

// TestResolveYBArchiveTargetRedoesAnExplicitRunWithoutItsInventory proves the
// same rule holds when the operator names the run, which is how an old run is
// healed by hand.
func TestResolveYBArchiveTargetRedoesAnExplicitRunWithoutItsInventory(t *testing.T) {
	ctx := context.Background()
	const runID = "20260830T010203Z"
	manifest := newYBSnapshotManifest(runID, "snap-1", "tack", []string{"yb1"}, ybTestArtifactNames())
	yb1 := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	objects := fakeYBExportRunObjects(t, runID, manifest)
	delete(objects, ybNodeInventoryKey(runID, yb1))
	s3Client, cfg := newFakeBackupObjectStore(t, "tack-backups", objects)

	target, err := resolveYBArchiveTarget(ctx, cfg, s3Client, "yb1", runID)
	if err != nil {
		t.Fatalf("resolve the explicit run for yb1: %v", err)
	}
	if target == nil {
		t.Fatal("an explicitly named run whose archive has no inventory must be redone")
	}
}
