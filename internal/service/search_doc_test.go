package service

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/domain/node"
)

func TestSearchDocKeepsTextPropertiesOnly(t *testing.T) {
	view := &node.NodeView{
		ID: uuid.New(), OrgID: uuid.New(), NodeType: "issue", Name: "Backup drill",
		Props: map[string]json.RawMessage{
			"description": json.RawMessage(`"Restore the ledger"`),
			"state_id":    json.RawMessage(`"0190c0de-0000-7000-8000-000000000000"`),
		},
	}
	defs := []*node.PropertyDef{
		{Name: "description", Type: node.PropertyTypeText},
		{Name: "state_id", Type: node.PropertyTypeUUID},
	}

	doc := SearchDocFromView(view, defs)

	if _, ok := doc.Props["state_id"]; ok {
		t.Fatal("uuid property was indexed")
	}
	if string(doc.Props["description"]) != `"Restore the ledger"` {
		t.Fatalf("description = %s", doc.Props["description"])
	}
}
