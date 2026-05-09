package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func TestAddressBackfillApplyWritesAndRerunIsIdempotent(t *testing.T) {
	nodeID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	inspector := &addressBackfillFakeInspector{
		rows: []fdbadapter.InspectLegacyAddressRow{legacyAddressRow(nodeID, "project", "TACK")},
		reports: map[uuid.UUID]*fdbadapter.NodeInspectionReport{
			nodeID: {NodeID: nodeID, Resolve: &node.NodeResolve{OrgID: orgID, NodeType: "project"}},
		},
	}
	addresses := newAddressBackfillFakeAddressStore()

	limitedRows, truncated := limitAddressBackfillRows(inspector.rows, 10)
	result, err := buildAddressBackfillResult(context.Background(), "backfill.addresses.apply", "apply", addressBackfillControls{Limit: 10}, limitedRows, truncated, inspector, addresses)
	if err != nil {
		t.Fatalf("buildAddressBackfillResult: %v", err)
	}
	if err := applyAddressBackfill(context.Background(), addresses, result); err != nil {
		t.Fatalf("applyAddressBackfill: %v", err)
	}
	if result.WrittenCount != 1 || len(addresses.writes) != 1 {
		t.Fatalf("written count = %d writes = %d, want 1 and 1", result.WrittenCount, len(addresses.writes))
	}

	rerunRows, rerunTruncated := limitAddressBackfillRows(inspector.rows, 10)
	rerun, err := buildAddressBackfillResult(context.Background(), "backfill.addresses.apply", "apply", addressBackfillControls{Limit: 10}, rerunRows, rerunTruncated, inspector, addresses)
	if err != nil {
		t.Fatalf("buildAddressBackfillResult rerun: %v", err)
	}
	if err := applyAddressBackfill(context.Background(), addresses, rerun); err != nil {
		t.Fatalf("applyAddressBackfill rerun: %v", err)
	}
	if rerun.IdempotentCount != 1 {
		t.Fatalf("rerun IdempotentCount = %d want 1", rerun.IdempotentCount)
	}
	if len(addresses.writes) != 1 {
		t.Fatalf("writes after rerun = %d want 1", len(addresses.writes))
	}
}

func TestAddressBackfillApplyStopsOnConflict(t *testing.T) {
	nodeID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	otherNodeID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	inspector := &addressBackfillFakeInspector{
		rows: []fdbadapter.InspectLegacyAddressRow{legacyAddressRow(nodeID, "project", "TACK")},
		reports: map[uuid.UUID]*fdbadapter.NodeInspectionReport{
			nodeID: {NodeID: nodeID, Resolve: &node.NodeResolve{OrgID: orgID, NodeType: "project"}},
		},
	}
	addresses := newAddressBackfillFakeAddressStore()
	addresses.addresses[addressBackfillFakeAddressKey("project", node.AddressKindPrimary, "TACK")] = otherNodeID

	limitedRows, truncated := limitAddressBackfillRows(inspector.rows, 10)
	result, err := buildAddressBackfillResult(context.Background(), "backfill.addresses.apply", "apply", addressBackfillControls{Limit: 10}, limitedRows, truncated, inspector, addresses)
	if err != nil {
		t.Fatalf("buildAddressBackfillResult: %v", err)
	}
	err = applyAddressBackfill(context.Background(), addresses, result)
	if err == nil || !strings.Contains(err.Error(), "blocked by 1 conflicts") {
		t.Fatalf("applyAddressBackfill err = %v want conflict block", err)
	}
	if result.ConflictCount != 1 || len(addresses.writes) != 0 {
		t.Fatalf("conflicts = %d writes = %d, want 1 and 0", result.ConflictCount, len(addresses.writes))
	}
}

func TestAddressBackfillApplyStopsOnMalformedCandidate(t *testing.T) {
	nodeID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	inspector := &addressBackfillFakeInspector{
		rows:    []fdbadapter.InspectLegacyAddressRow{legacyAddressRow(nodeID, "project", "TACK")},
		reports: map[uuid.UUID]*fdbadapter.NodeInspectionReport{nodeID: {NodeID: nodeID}},
	}
	addresses := newAddressBackfillFakeAddressStore()

	limitedRows, truncated := limitAddressBackfillRows(inspector.rows, 10)
	result, err := buildAddressBackfillResult(context.Background(), "backfill.addresses.apply", "apply", addressBackfillControls{Limit: 10}, limitedRows, truncated, inspector, addresses)
	if err != nil {
		t.Fatalf("buildAddressBackfillResult: %v", err)
	}
	err = applyAddressBackfill(context.Background(), addresses, result)
	if err == nil || !strings.Contains(err.Error(), "1 malformed") {
		t.Fatalf("applyAddressBackfill err = %v want malformed block", err)
	}
	if result.MalformedCount != 1 || len(addresses.writes) != 0 {
		t.Fatalf("malformed = %d writes = %d, want 1 and 0", result.MalformedCount, len(addresses.writes))
	}
}
