package integration_test

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityRepairRejectsLegacyParentInstructions requires operators
// to convert old requests and freeze new evidence before executing a repair.
func TestNodeIntegrityRepairRejectsLegacyParentInstructions(t *testing.T) {
	env := integration.SetupTestEnv(t)
	graph := newRepairGraph(t, env)
	entry := graph.selectEpicEntry("legacy-parent-instruction")
	request := repairRequest(entry)
	request.Version = 1
	before := storedKeyValues(t)
	result, err := runRepairCommand(
		t,
		"--request",
		writeRepairDocument(t, "legacy-request.json", request),
	)
	if err == nil || !strings.Contains(err.Error(), "manifest version 1 is not 2") ||
		result.Manifest != nil {
		t.Fatalf("legacy request returned %+v, %v, want version rejection", result, err)
	}

	request = repairRequest(entry)
	request.Entries[0].Result.ParentID = nil
	request.Entries[0].Result.Props = map[string]json.RawMessage{
		"parent_id": textValue(graph.epicID.String()),
	}
	result, err = runRepairCommand(
		t,
		"--request",
		writeRepairDocument(t, "legacy-property.json", request),
	)
	if err == nil || !strings.Contains(err.Error(), "result.props.parent_id is obsolete") ||
		result.Manifest != nil {
		t.Fatalf("legacy parent property returned %+v, %v, want instruction rejection", result, err)
	}

	manifest := freezeRepairRequest(t, repairRequest(entry))
	manifest.Version = 1
	manifest.Digest = manifestDigest(t, manifest)
	result, err = runRepairCommand(
		t,
		"--execute",
		"--manifest",
		writeRepairDocument(t, "legacy-manifest.json", manifest),
	)
	if err == nil || !strings.Contains(err.Error(), "manifest version 1 is not 2") ||
		result.Applied != nil {
		t.Fatalf("legacy frozen manifest returned %+v, %v, want version rejection", result, err)
	}

	if !maps.Equal(before, storedKeyValues(t)) {
		t.Fatal("a legacy parent instruction changed stored keys or values")
	}
}
