package integration_test

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairRefusesPathsOutsideRepairDirectory checks path limits.
// It freezes a request with one delete entry that sets an export file path
// in a temporary directory outside the repair directory. It checks that the
// command refuses the request without producing a manifest.
//
// It then freezes a valid delete entry with an export file path inside the
// repair directory, writes the manifest to a file outside the repair
// directory, and creates a symbolic link to that file inside the repair
// directory.
//
// For both the outside path and the link path, it checks that the command
// refuses --execute and does not return an apply result. It verifies that
// the command leaves every stored FoundationDB key and value unchanged,
// keeps the node, and does not write the export file.
func TestNodeIntegrityRepairRefusesPathsOutsideRepairDirectory(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	outside := t.TempDir()
	escaped := deleteFixtureEntry(t, env, graph, "tack-582-export-outside", filepath.Join(outside, "export.json"))
	result, err := runRepairCommand(t, "--request", writeRepairDocument(t, "outside.json", repairRequest(escaped)))
	if err == nil || result.Manifest != nil || !strings.Contains(err.Error(), "outside the repair directory") {
		t.Fatalf("freeze with an export file outside the repair directory = %+v, %v, want a refusal", result, err)
	}

	inside := deleteFixtureEntry(t, env, graph, "tack-582-export-inside", filepath.Join(repairDirectory(t), "export.json"))
	manifest := freezeRepairRequest(t, repairRequest(inside))
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode the manifest: %v", err)
	}
	target := filepath.Join(outside, "manifest.json")
	if err := os.WriteFile(target, encoded, 0o600); err != nil {
		t.Fatalf("write the manifest outside the repair directory: %v", err)
	}
	link := filepath.Join(repairDirectory(t), "linked.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("link %s to %s: %v", link, target, err)
	}
	before := storedKeyValues(t)
	for _, path := range []string{target, link} {
		refused, err := runRepairCommand(t, "--execute", "--manifest", path)
		if err == nil || refused.Applied != nil || !strings.Contains(err.Error(), "outside the repair directory") {
			t.Fatalf("execute the manifest at %s = %+v, %v, want a refusal", path, refused, err)
		}
	}
	if !maps.Equal(before, storedKeyValues(t)) {
		t.Fatal("a refused execution changed stored keys or values")
	}
	requireRepairNodePresent(t, env, inside.NodeID)
	if _, err := os.Stat(inside.Delete.ExportFile); !os.IsNotExist(err) {
		t.Fatalf("stat the export file %s = %v, want no file", inside.Delete.ExportFile, err)
	}
}
