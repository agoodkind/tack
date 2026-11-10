// backup_yb_node_artifacts.go declares what one data guest's archive run
// publishes under its node prefix, and is the completeness gate every reader of
// an export run consults. One declaration serves both sides: the archive
// command uploads exactly these objects in this order, and the gate requires
// exactly these objects to be present, so a new node artifact is gated by
// adding it here rather than by remembering to probe for it.

package ops

// ybNodeArtifactObjects names every object one node's archive run publishes,
// in upload order. The tablet archive is last so that no archive is ever in
// the store without the inventory recorded from it. The archive command's own
// idempotency probe requires every object here, the same rule as the gate, so
// a run interrupted between the two uploads, or an archive published before
// inventories existed, looks unarchived on the next timer and is redone.
func ybNodeArtifactObjects() []string {
	return []string{ybNodeInventoryObject, ybNodeArchiveObject}
}

// ybNodeArtifactKeys is the full object key of every artifact the named node
// must publish for the manifest's run.
func ybNodeArtifactKeys(runID string, node ybSnapshotManifestNode) []string {
	prefix := ybNodeKeyPrefix(runID, node)
	keys := make([]string, 0, len(ybNodeArtifactObjects()))
	for _, object := range ybNodeArtifactObjects() {
		keys = append(keys, prefix+object)
	}
	return keys
}

// ybNodeKeyPrefix is the object-store key prefix one node's artifacts live
// under for a run.
func ybNodeKeyPrefix(runID string, node ybSnapshotManifestNode) string {
	return ybSnapshotKeyPrefix(runID) + node.Prefix
}

// ybNodeArchiveKey is the full object key of one node's tablet archive for the
// manifest's run. It is the object the completeness gate's node names are
// reported against and the one the restore stages.
func ybNodeArchiveKey(runID string, node ybSnapshotManifestNode) string {
	return ybNodeKeyPrefix(runID, node) + ybNodeArchiveObject
}

// ybNodeInventoryKey is the full object key of the inventory recording what
// that node's archive carries.
func ybNodeInventoryKey(runID string, node ybSnapshotManifestNode) string {
	return ybNodeKeyPrefix(runID, node) + ybNodeInventoryObject
}

// missingYBNodeArchives returns the names of the manifest's nodes that have not
// published every artifact their archive run owes, using the caller's existence
// check so the completeness rule stays testable without an object store. A node
// counts as archived only once all of them are present: an archive with no
// inventory beside it is an archive the restore cannot verify, so it is as
// incomplete as no archive at all.
func missingYBNodeArchives(manifest ybSnapshotManifest, exists func(key string) (bool, error)) ([]string, error) {
	var missing []string
	for _, node := range manifest.Nodes {
		complete, err := ybNodeFullyArchived(manifest.RunID, node, exists)
		if err != nil {
			return nil, err
		}
		if !complete {
			missing = append(missing, node.Name)
		}
	}
	return missing, nil
}

// ybNodeFullyArchived reports whether every object one node owes the run is
// present.
func ybNodeFullyArchived(
	runID string,
	node ybSnapshotManifestNode,
	exists func(key string) (bool, error),
) (bool, error) {
	for _, key := range ybNodeArtifactKeys(runID, node) {
		present, err := exists(key)
		if err != nil {
			return false, err
		}
		if !present {
			return false, nil
		}
	}
	return true, nil
}
