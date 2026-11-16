package ops

import (
	"encoding/json"
	"testing"
)

// ybExportRunObjects builds a finished export's manifest, root artifacts, and
// per-node artifacts. It stores the manifest under prefixRunID independently
// of the manifest's run ID to test mismatched prefixes.
func ybExportRunObjects(
	t *testing.T,
	prefixRunID string,
	manifest ybSnapshotManifest,
) map[string][]byte {
	t.Helper()
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal yb snapshot manifest: %v", err)
	}

	prefix := ybSnapshotKeyPrefix(prefixRunID)
	objects := map[string][]byte{prefix + ybSnapshotManifestObject: body}

	for _, artifact := range manifest.Artifacts {
		objects[prefix+artifact] = []byte("export artifact " + artifact)
	}

	for _, node := range manifest.Nodes {
		for _, object := range ybNodeArtifactObjects() {
			objects[prefix+node.Prefix+object] = ybNodeArtifactBody(manifest, node, object)
		}
	}

	return objects
}

// ybNodeArtifactBody uses the production inventory writer for an empty file
// list with the manifest's run and node IDs. Other artifacts use opaque bytes.
func ybNodeArtifactBody(
	manifest ybSnapshotManifest,
	node ybSnapshotManifestNode,
	object string,
) []byte {
	if object == ybNodeInventoryObject {
		return ybArchiveInventory{RunID: manifest.RunID, Node: node.Name, Files: nil}.render()
	}

	return []byte("node artifact")
}
