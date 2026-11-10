package ops

import (
	"context"
	"testing"
)

// TestResolveYBArchiveTargetRedoesAnArchiveWithoutItsInventory reproduces
// the legacy archive state that the earlier archive-only check accepted.
// That check left archives without inventories and caused the restore drill
// to reject their runs. The test uses SeaweedFS with an archive but without
// an inventory for yb1; it must require another upload. The yb2 prefix
// contains both artifacts and must resolve as complete.
func TestResolveYBArchiveTargetRedoesAnArchiveWithoutItsInventory(t *testing.T) {
	ctx := context.Background()
	const runID = "20260830T010203Z"
	manifest := newYBSnapshotManifest(
		runID,
		"snap-1",
		"tack",
		[]string{"yb1", "yb2"},
		ybTestArtifactNames(),
	)
	yb1 := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	objects := ybExportRunObjects(t, runID, manifest)
	delete(objects, ybNodeInventoryKey(runID, yb1))
	store := newBackupTestStore(t, objects)
	s3Client, cfg := store.client, store.config()

	target, err := resolveYBArchiveTarget(ctx, cfg, s3Client, "yb1", "")
	if err != nil {
		t.Fatalf("resolve the target for yb1: %v", err)
	}

	if target == nil || target.complete {
		t.Fatal(
			"an archive in the store without its inventory must be redone, but yb1 found nothing to do",
		)
	}

	if target.manifest.RunID != runID || target.prefix != ybNodeKeyPrefix(runID, yb1) {
		t.Fatalf(
			"yb1 resolved run %q prefix %q, want run %q prefix %q",
			target.manifest.RunID,
			target.prefix,
			runID,
			ybNodeKeyPrefix(runID, yb1),
		)
	}

	done, err := resolveYBArchiveTarget(ctx, cfg, s3Client, "yb2", "")
	if err != nil {
		t.Fatalf("resolve the target for yb2: %v", err)
	}

	if done == nil || !done.complete ||
		done.prefix != ybNodeKeyPrefix(
			runID,
			ybSnapshotManifestNode{Name: "yb2", Prefix: "nodes/yb2/"},
		) {
		t.Fatalf(
			"a node with every artifact in the store must resolve its complete prefix, got %+v",
			done,
		)
	}
}

// TestResolveYBArchiveTargetRedoesAnExplicitRunWithoutItsInventory checks
// that an explicit run ID also requires another upload when its inventory
// is missing. An operator can select an older run for this repair.
func TestResolveYBArchiveTargetRedoesAnExplicitRunWithoutItsInventory(t *testing.T) {
	ctx := context.Background()
	const runID = "20260830T010203Z"
	manifest := newYBSnapshotManifest(
		runID,
		"snap-1",
		"tack",
		[]string{"yb1"},
		ybTestArtifactNames(),
	)
	yb1 := ybSnapshotManifestNode{Name: "yb1", Prefix: "nodes/yb1/"}
	objects := ybExportRunObjects(t, runID, manifest)
	delete(objects, ybNodeInventoryKey(runID, yb1))
	store := newBackupTestStore(t, objects)
	s3Client, cfg := store.client, store.config()

	target, err := resolveYBArchiveTarget(ctx, cfg, s3Client, "yb1", runID)
	if err != nil {
		t.Fatalf("resolve the explicit run for yb1: %v", err)
	}

	if target == nil || target.complete {
		t.Fatal("an explicitly named run with an archive and no inventory must be redone")
	}
}
