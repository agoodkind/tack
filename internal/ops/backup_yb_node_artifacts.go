// One declaration defines the artifacts that a data guest uploads under its
// node prefix. The archive command uploads those objects in declaration order,
// and readers require every declared object before treating the node's export
// as complete. The declaration includes each added artifact in both operations.

package ops

// ybNodeArtifactObjects returns the objects a node uploads, in upload order.
// The inventory precedes the tablet archive. The archive command's repeat-run
// check requires every declared object, as the completeness check does. The
// next timer retries a run interrupted between uploads or an older archive that
// predates inventories.
func ybNodeArtifactObjects() []string {
	return []string{ybNodeInventoryObject, ybNodeArchiveObject}
}

// ybNodeArtifactKeys returns the complete object key for every artifact the
// node must publish for the manifest's run.
func ybNodeArtifactKeys(runID string, node ybSnapshotManifestNode) []string {
	prefix := ybNodeKeyPrefix(runID, node)

	keys := make([]string, 0, len(ybNodeArtifactObjects()))
	for _, object := range ybNodeArtifactObjects() {
		keys = append(keys, prefix+object)
	}

	return keys
}

// ybNodeKeyPrefix returns the object-store prefix for a node's artifacts in one
// run.
func ybNodeKeyPrefix(runID string, node ybSnapshotManifestNode) string {
	return ybSnapshotKeyPrefix(runID) + node.Prefix
}

// ybNodeArchiveKey returns the complete object key for a node's tablet archive
// in the manifest's run. Restore stages this object, and the completeness check
// reports the node when its required artifacts are missing.
func ybNodeArchiveKey(runID string, node ybSnapshotManifestNode) string {
	return ybNodeKeyPrefix(runID, node) + ybNodeArchiveObject
}

// ybNodeInventoryKey returns the complete object key for the inventory that
// records a node archive's contents.
func ybNodeInventoryKey(runID string, node ybSnapshotManifestNode) string {
	return ybNodeKeyPrefix(runID, node) + ybNodeInventoryObject
}

// missingYBNodeArchives returns manifest node names when required artifacts are
// missing. It uses the caller's existence check, which permits tests without an
// object store. A complete node requires every artifact, including the
// inventory that restore uses to verify the archive. An archive without an
// inventory does not satisfy that requirement.
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

// ybNodeFullyArchived reports whether every required object exists for one node
// and run.
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
