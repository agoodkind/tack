package integration_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// TestNodeIntegrityStrictRefusesActivationWithInvalidRecord stores a node
// record with an undeclared property through test-only storage setup. It
// checks that both the dry run and the executed command report the invalid
// record. It also checks that the executed command fails and strict
// validation remains inactive.
func TestNodeIntegrityStrictRefusesActivationWithInvalidRecord(t *testing.T) {
	env := integration.SetupTestEnv(t)
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	props := map[string]json.RawMessage{"strict_test_undeclared": json.RawMessage(strconv.Quote("value"))}
	record := &node.Node{
		ID: nodeID, OrgID: env.OrgID, NodeType: "workspace", Name: "invalid workspace", Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: nodeID, OrgID: env.OrgID, NodeType: "workspace", Name: record.Name, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, record, view, nil, nil, nil, nil); err != nil {
		t.Fatalf("store the invalid record: %v", err)
	}
	for _, execute := range []bool{false, true} {
		output, err := runStrictValidation(t, execute)
		if err == nil {
			t.Fatalf("strict validation with execute %t succeeded, want a refusal", execute)
		}
		if output.Result.Activated || output.Result.ViolationCount == 0 || !reportsViolation(output, nodeID) {
			t.Fatalf("strict validation with execute %t = %+v, want node %s reported and no activation", execute, output.Result, nodeID)
		}
	}
	activation, err := env.Stores.Nodes.StrictActivation(env.Ctx)
	if err != nil || activation.Active {
		t.Fatalf("activation after the refusals = %+v, %v; want inactive", activation, err)
	}
}

// TestNodeIntegrityStrictMetadataChangeInvalidatesEvidence captures evidence
// and changes a property definition. It checks that ActivateStrict rejects
// the evidence and reports the organization. It then checks that the command
// rescans and activates strict validation after the evidence stops changing.
// A second command run reports the existing activation.
func TestNodeIntegrityStrictMetadataChangeInvalidatesEvidence(t *testing.T) {
	env := integration.SetupTestEnv(t)
	evidence, err := env.Stores.Nodes.CaptureStrictEvidence(env.Ctx)
	if err != nil {
		t.Fatalf("capture the evidence: %v", err)
	}
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: env.OrgID, Name: "strict_test_label", Type: node.PropertyType("text"),
		AppliesToFeatures: nil, Indexed: false, Options: nil, Required: false, DefaultValue: nil,
		DefaultReference: nil, ReferenceTargetTypeKey: "", Search: &node.SearchProjection{Include: false, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
	}
	if err := env.Stores.PropertyDefs.Set(env.Ctx, definition); err != nil {
		t.Fatalf("store the property definition: %v", err)
	}
	activation := node.StrictActivation{Active: true, ActivatedAt: clock.Now().UTC(), ScanID: uuid.Must(uuid.NewV7()), RecordsValidated: 0}
	change, err := env.Stores.Nodes.ActivateStrict(env.Ctx, evidence, activation)
	if !errors.Is(err, node.ErrStrictEvidenceChanged) || !slices.Contains(change.Organizations, env.OrgID) {
		t.Fatalf("activation on stale evidence = %+v, %v; want ErrStrictEvidenceChanged for %s", change, err, env.OrgID)
	}
	output, err := runStrictValidation(t, true)
	if err != nil || !output.Result.Activated || output.Result.ViolationCount != 0 {
		t.Fatalf("strict validation = %+v, %v; want activation without violations", output.Result, err)
	}
	stored, err := env.Stores.Nodes.StrictActivation(env.Ctx)
	if err != nil || !stored.Active || stored.ScanID != output.Result.ScanID {
		t.Fatalf("stored activation = %+v, %v; want scan %s active", stored, err, output.Result.ScanID)
	}
	again, err := runStrictValidation(t, true)
	if err != nil || !again.Result.AlreadyActive || again.Result.Activated {
		t.Fatalf("second strict validation = %+v, %v; want the existing activation reported", again.Result, err)
	}
}

func reportsViolation(output strictValidationOutput, nodeID uuid.UUID) bool {
	for _, violation := range output.Result.FirstViolations {
		if violation.NodeID == nodeID {
			return true
		}
	}
	return false
}
