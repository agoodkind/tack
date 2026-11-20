package integration_test

import (
	"encoding/json"
	"maps"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/test/integration"
)

// executeRepairManifest runs the command with --execute for manifest and
// returns its apply result. It fails the test when the command does not
// return an apply result.
func executeRepairManifest(
	t *testing.T,
	manifest ops.NodeIntegrityManifest,
) (ops.NodeIntegrityApply, error) {
	t.Helper()
	result, err := runRepairCommand(
		t,
		"--execute",
		"--manifest",
		writeRepairDocument(t, "manifest.json", manifest),
	)
	if result.Applied == nil {
		t.Fatalf("execute = %v without an apply result", err)
	}

	return *result.Applied, err
}

func requireApplyStatus(
	t *testing.T,
	applied ops.NodeIntegrityApply,
	repairID string,
	status ops.RepairApplyStatus,
) ops.NodeIntegrityEntryApply {
	t.Helper()

	for _, entry := range applied.Entries {
		if entry.RepairID == repairID {
			if entry.Status != status {
				t.Fatalf("apply entry %s = %+v, want status %s", repairID, entry, status)
			}

			return entry
		}
	}

	t.Fatalf("apply result has no entry %s: %+v", repairID, applied.Entries)

	return ops.NodeIntegrityEntryApply{}
}

// requireUnchangedRerun executes manifest again and requires the execution to
// report every entry as unchanged. The preview must report every entry as
// completed, and every stored FoundationDB key and value must remain unchanged.
func requireUnchangedRerun(t *testing.T, manifest ops.NodeIntegrityManifest) {
	t.Helper()
	before := storedKeyValues(t)
	applied, err := executeRepairManifest(t, manifest)
	if err != nil || applied.Counts[ops.RepairApplyUnchanged] != len(manifest.Entries) {
		t.Fatalf(
			"second execution = %v with counts %v, want every entry unchanged",
			err,
			applied.Counts,
		)
	}

	preview := previewRepairManifest(t, manifest)
	if preview.Counts[ops.RepairPreviewCompleted] != len(manifest.Entries) {
		t.Fatalf("preview after completion counts = %v, want every entry completed", preview.Counts)
	}

	if !maps.Equal(before, storedKeyValues(t)) {
		t.Fatal("the second execution changed stored keys or values")
	}
}

// setLegacyProps writes props over the stored properties of nodeID without
// validation, as an unchecked writer did before TACK-582. A JSON null removes
// a property.
func setLegacyProps(
	t *testing.T,
	env *integration.TestEnv,
	nodeID uuid.UUID,
	props map[string]json.RawMessage,
) {
	t.Helper()
	current, err := env.Stores.Nodes.Get(env.Ctx, env.OrgID, nodeID)
	if err != nil || current == nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}

	view, err := env.Stores.Views.Get(env.Ctx, nodeID)
	if err != nil || view == nil {
		t.Fatalf("read view %s: %v", nodeID, err)
	}

	previous := maps.Clone(current.Props)

	for name, value := range props {
		if string(value) == "null" {
			delete(current.Props, name)
			continue
		}

		current.Props[name] = value
	}

	view.Props = maps.Clone(current.Props)
	if err := env.Stores.Nodes.UpdateAtomic(
		env.Ctx,
		current,
		view,
		previous,
		nil,
		nil,
	); err != nil {
		t.Fatalf("write legacy properties of node %s: %v", nodeID, err)
	}
}

// propText decodes the JSON string property with key name in props. It
// returns an empty string when the property is absent.
func propText(t *testing.T, props map[string]json.RawMessage, name string) string {
	t.Helper()
	raw, present := props[name]
	if !present {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		t.Fatalf("property %s = %s, want a JSON string", name, raw)
	}

	return text
}

func textValue(value string) json.RawMessage {
	return json.RawMessage(strconv.Quote(value))
}
