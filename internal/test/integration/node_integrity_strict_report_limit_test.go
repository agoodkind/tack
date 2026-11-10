package integration_test

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// strictReportInvalidRecords is larger than the default report limit of 20.
const strictReportInvalidRecords = 25

// TestNodeIntegrityStrictReportListsMaxViolations stores more invalid node
// records than the default report limit. It checks that --max-violations
// sets how many records the report lists in a dry run and with --execute,
// and that violation_count still counts every invalid record. It also checks
// that a value outside the allowed range fails before the scan and that
// strict validation stays inactive.
func TestNodeIntegrityStrictReportListsMaxViolations(t *testing.T) {
	env := integration.SetupTestEnv(t)
	stored := map[uuid.UUID]bool{}
	for i := range strictReportInvalidRecords {
		stored[storeInvalidStrictRecord(t, env, i)] = true
	}
	runs := []struct {
		execute bool
		limit   int
		listed  int
	}{
		{execute: false, limit: 22, listed: 22},
		{execute: true, limit: 30, listed: strictReportInvalidRecords},
	}
	for _, run := range runs {
		output, err := runStrictValidation(t, run.execute, "--max-violations="+strconv.Itoa(run.limit))
		if run.execute == (err == nil) {
			t.Fatalf("strict validation with execute %t and limit %d returned error %v", run.execute, run.limit, err)
		}
		result := output.Result
		if result.Activated || result.ViolationCount != strictReportInvalidRecords || len(result.FirstViolations) != run.listed {
			t.Fatalf("strict validation with execute %t and limit %d = %d listed of %d, activated %t; want %d listed of %d",
				run.execute, run.limit, len(result.FirstViolations), result.ViolationCount, result.Activated,
				run.listed, strictReportInvalidRecords)
		}
		if output.MaxViolations != run.limit {
			t.Fatalf("strict validation max_violations = %d, want %d", output.MaxViolations, run.limit)
		}
		for _, violation := range result.FirstViolations {
			if !stored[violation.NodeID] {
				t.Fatalf("strict validation listed node %s, which the test did not store as invalid", violation.NodeID)
			}
		}
	}
	for _, rejected := range []string{"0", "5001"} {
		output, err := runStrictValidationOutput(t, false, "--max-violations="+rejected)
		if err == nil || !strings.Contains(err.Error(), "--max-violations") {
			t.Fatalf("strict validation with --max-violations=%s returned error %v, want a range error", rejected, err)
		}
		if bytes.Contains(output, []byte("records_validated")) {
			t.Fatalf("strict validation with --max-violations=%s printed a scan report: %s", rejected, output)
		}
	}
	activation, err := env.Stores.Nodes.StrictActivation(env.Ctx)
	if err != nil || activation.Active {
		t.Fatalf("activation after the runs = %+v, %v; want inactive", activation, err)
	}
}

// storeInvalidStrictRecord stores a workspace with an undeclared property
// through test-only storage setup and returns its node ID.
func storeInvalidStrictRecord(t *testing.T, env *integration.TestEnv, index int) uuid.UUID {
	t.Helper()
	nodeID := uuid.Must(uuid.NewV7())
	now := clock.Now().UTC()
	name := "invalid workspace " + strconv.Itoa(index)
	props := map[string]json.RawMessage{"strict_test_undeclared": json.RawMessage(strconv.Quote("value"))}
	record := &node.Node{
		ID: nodeID, OrgID: env.OrgID, NodeType: "workspace", Name: name, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	view := &node.NodeView{
		ID: nodeID, OrgID: env.OrgID, NodeType: "workspace", Name: name, Props: props,
		CreatedBy: uuid.Nil, UpdatedBy: uuid.Nil, CreatedAt: now, UpdatedAt: now,
	}
	if err := env.Stores.Nodes.CreateAtomic(env.Ctx, record, view, nil, nil, nil, nil); err != nil {
		t.Fatalf("store invalid record %d: %v", index, err)
	}
	return nodeID
}
