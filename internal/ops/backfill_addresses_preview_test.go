package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/domain/node"
)

func TestAddressBackfillPreviewMapsLegacyRowsToPrimaryAddressTargets(t *testing.T) {
	firstNodeID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	secondNodeID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	inspector := &addressBackfillFakeInspector{
		rows: []fdbadapter.InspectLegacyAddressRow{
			legacyAddressRow(secondNodeID, "zeta", "ZETA"),
			legacyAddressRow(firstNodeID, "alpha", "ALPHA"),
		},
		reports: map[uuid.UUID]*fdbadapter.NodeInspectionReport{
			firstNodeID:  {NodeID: firstNodeID, Resolve: &node.NodeResolve{OrgID: orgID, NodeType: "alpha"}},
			secondNodeID: {NodeID: secondNodeID, Resolve: &node.NodeResolve{OrgID: orgID, NodeType: "zeta"}},
		},
	}
	addresses := newAddressBackfillFakeAddressStore()

	limitedRows, truncated := limitAddressBackfillRows(inspector.rows, 10)
	result, err := buildAddressBackfillResult(
		context.Background(),
		"backfill.addresses.preview",
		"preview",
		addressBackfillControls{Limit: 10},
		limitedRows,
		truncated,
		inspector,
		addresses,
	)
	if err != nil {
		t.Fatalf("buildAddressBackfillResult: %v", err)
	}
	if result.CandidateCount != 2 || result.WriteCount != 2 {
		t.Fatalf("counts = candidates %d writes %d, want 2 and 2", result.CandidateCount, result.WriteCount)
	}
	candidate := result.Candidates[0]
	if candidate.Target.NodeType != "alpha" || candidate.Target.AddressValue != "ALPHA" {
		t.Fatalf("first candidate target = %#v, want alpha/ALPHA", candidate.Target)
	}
	if candidate.Target.KeyFamily != "address_index" {
		t.Fatalf("target key family = %q want address_index", candidate.Target.KeyFamily)
	}
	if candidate.Target.AddressKind != string(node.AddressKindPrimary) {
		t.Fatalf("target address kind = %q want %s", candidate.Target.AddressKind, node.AddressKindPrimary)
	}
	if candidate.Status != addressBackfillStatusWrite {
		t.Fatalf("candidate status = %q want write", candidate.Status)
	}
}

func TestAddressBackfillPreviewRespectsLimit(t *testing.T) {
	firstNodeID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	secondNodeID := uuid.MustParse("00000000-0000-0000-0000-000000000002")
	orgID := uuid.MustParse("00000000-0000-0000-0000-000000000011")
	inspector := &addressBackfillFakeInspector{
		rows: []fdbadapter.InspectLegacyAddressRow{
			legacyAddressRow(firstNodeID, "alpha", "ALPHA"),
			legacyAddressRow(secondNodeID, "beta", "BETA"),
		},
		reports: map[uuid.UUID]*fdbadapter.NodeInspectionReport{
			firstNodeID:  {NodeID: firstNodeID, Resolve: &node.NodeResolve{OrgID: orgID, NodeType: "alpha"}},
			secondNodeID: {NodeID: secondNodeID, Resolve: &node.NodeResolve{OrgID: orgID, NodeType: "beta"}},
		},
	}

	limitedRows, truncated := limitAddressBackfillRows(inspector.rows, 1)
	result, err := buildAddressBackfillResult(
		context.Background(),
		"backfill.addresses.preview",
		"preview",
		addressBackfillControls{Limit: 1},
		limitedRows,
		truncated,
		inspector,
		newAddressBackfillFakeAddressStore(),
	)
	if err != nil {
		t.Fatalf("buildAddressBackfillResult: %v", err)
	}
	if !result.Truncated {
		t.Fatal("Truncated = false want true")
	}
	if result.CandidateCount != 1 {
		t.Fatalf("CandidateCount = %d want 1", result.CandidateCount)
	}
}

func TestAddressBackfillCommandsAreRegisteredAndDocumented(t *testing.T) {
	if _, ok := Get("backfill.addresses.preview"); !ok {
		t.Fatal("backfill.addresses.preview is not registered")
	}
	if _, ok := Get("backfill.addresses.apply"); !ok {
		t.Fatal("backfill.addresses.apply is not registered")
	}
	stderr := captureRepairStderr(t, printBatchUsage)
	if !strings.Contains(stderr, "backfill.addresses.apply") {
		t.Fatalf("batch usage = %q want backfill.addresses.apply", stderr)
	}
	if !strings.Contains(stderr, backfillAddressApplyEnv+"=true") {
		t.Fatalf("batch usage = %q want apply env", stderr)
	}
}
