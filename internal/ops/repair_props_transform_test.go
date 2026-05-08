package ops

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestPreviewPropsTransformRenamesAppendsAndRemovesFields(t *testing.T) {
	nodeID := uuid.New()
	reader := &repairReader{views: map[uuid.UUID]*node.NodeView{
		nodeID: {
			ID:        nodeID,
			OrgID:     uuid.New(),
			NodeType:  "ticket",
			Name:      "Ticket",
			UpdatedAt: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC),
			Props: map[string]json.RawMessage{
				"Description": mustRaw(t, "Imported description"),
				"status":      mustRaw(t, "Completed"),
				"progress":    mustRaw(t, "100%"),
			},
		},
	}}
	console := NewRepairConsole(&repairNodeRepo{reader: reader}, reader, &repairTypeRepo{types: repairTypes()}, &repairPropRepo{defs: repairDefs()}, &repairSearcher{})
	profile := &RepairReferenceProfile{
		Name:         "legacy-props",
		RemoveFields: []string{"progress"},
		RenameFields: []RepairRenameField{{From: "Description", To: "description"}},
		AppendTextFields: []RepairAppendTextField{{
			From:    "status",
			To:      "description",
			Heading: "Imported status",
		}},
	}

	preview, err := console.Preview(context.Background(), RepairPreviewInput{Class: RepairClassPropsTransform, NodeID: nodeID, Profile: profile})
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !preview.CanApply {
		t.Fatal("Preview CanApply = false want true")
	}
	if got := rawStringValue(preview.PlannedProps["description"]); !strings.Contains(got, "Imported status\nCompleted") {
		t.Fatalf("planned description = %q want appended status", got)
	}
	for _, field := range []string{"Description", "status", "progress"} {
		if string(preview.PlannedProps[field]) != "null" {
			t.Fatalf("planned %s = %s want null", field, preview.PlannedProps[field])
		}
	}
}
