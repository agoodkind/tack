package ops

import (
	"encoding/json"
	"testing"
)

// ybExportRunObjects builds the object set for a finished export under one
// run prefix. It includes the manifest, every declared artifact at the run
// root, and every node artifact that the completeness check requires. It
// stores the manifest under prefixRunID even if the manifest declares another
// run, allowing tests of that mismatch.
func ybExportRunObjects(t *testing.T, prefixRunID string, manifest ybSnapshotManifest) map[string][]byte {
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

// ybNodeArtifactBody returns bytes for a node artifact in the test store.
// It uses the production inventory renderer for the manifest's run and node
// with an empty file list. The drill staging step can read that inventory as
// it reads a real one. Other artifacts contain opaque fixture bytes.
func ybNodeArtifactBody(manifest ybSnapshotManifest, node ybSnapshotManifestNode, object string) []byte {
	if object == ybNodeInventoryObject {
		return ybArchiveInventory{RunID: manifest.RunID, Node: node.Name, Files: nil}.render()
	}
	return []byte("node artifact")
}
