// Node artifact declarations control archive upload order and export
// completeness checks. Both operations use the same list of required objects.

package ops

// ybNodeArtifactObjects lists node artifacts in upload order. Uploading the
// inventory first prevents the archive from indicating completion before its
// inventory exists. Idempotency checks require both objects and retry
// interrupted uploads or archives that predate inventories.
func ybNodeArtifactObjects() []string {
	return []string{ybNodeInventoryObject, ybNodeArchiveObject}
}

// ybNodeArtifactKeys returns required artifact keys for a node in this run.
func ybNodeArtifactKeys(runID string, node ybSnapshotManifestNode) []string {
	prefix := ybNodeKeyPrefix(runID, node)

	keys := make([]string, 0, len(ybNodeArtifactObjects()))
	for _, object := range ybNodeArtifactObjects() {
		keys = append(keys, prefix+object)
	}

	return keys
}

// ybNodeKeyPrefix returns the node's artifact prefix for a run.
func ybNodeKeyPrefix(runID string, node ybSnapshotManifestNode) string {
	return ybSnapshotKeyPrefix(runID) + node.Prefix
}

// ybNodeArchiveKey returns the archive key for completeness diagnostics and
// restore staging.
func ybNodeArchiveKey(runID string, node ybSnapshotManifestNode) string {
	return ybNodeKeyPrefix(runID, node) + ybNodeArchiveObject
}

// ybNodeInventoryKey returns the inventory key for the node's archived files.
func ybNodeInventoryKey(runID string, node ybSnapshotManifestNode) string {
	return ybNodeKeyPrefix(runID, node) + ybNodeInventoryObject
}

// missingYBNodeArchives returns nodes missing any required artifact, using the
// caller's existence check. An archive without an inventory remains incomplete
// because the restore cannot verify its files.
func missingYBNodeArchives(
	manifest ybSnapshotManifest,
	exists func(key string) (bool, error),
) ([]string, error) {
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

// ybNodeFullyArchived reports whether every required node artifact exists.
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
