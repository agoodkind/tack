package ops

import (
	"encoding/json"
	"testing"
)

// fakeYBExportRunObjects is the object set a finished export run leaves under
// one run prefix: the manifest the walk reads, every run-root artifact the
// manifest declares, and, per node the manifest lists, every artifact that
// node's archive run publishes, which is what the completeness gate probes for.
// The manifest is placed under prefixRunID whatever run it declares, so a
// manifest that names a run other than its own prefix can be exercised.
func fakeYBExportRunObjects(t *testing.T, prefixRunID string, manifest ybSnapshotManifest) map[string][]byte {
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
			objects[prefix+node.Prefix+object] = fakeYBNodeArtifact(manifest, node, object)
		}
	}
	return objects
}

// fakeYBNodeArtifact is the body of one node artifact in the fake store. The
// inventory is rendered through the production writer for the manifest's own
// run and node, recording no files, so the drill's staging step reads it back
// the way it reads a real one; every other node artifact is opaque bytes.
func fakeYBNodeArtifact(manifest ybSnapshotManifest, node ybSnapshotManifestNode, object string) []byte {
	if object == ybNodeInventoryObject {
		return ybArchiveInventory{RunID: manifest.RunID, Node: node.Name, Files: nil}.render()
	}
	return []byte("node artifact")
}
