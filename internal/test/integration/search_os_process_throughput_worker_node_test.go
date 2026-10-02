package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/service"
)

func createThroughputWorkerNode(t *testing.T, fixture queryFixture, kind opaqueKind, parent, actor uuid.UUID, name, content string) uuid.UUID {
	t.Helper()
	stores := fixture.Stores
	svc := service.NewNodeService(stores.Nodes, stores.Views, stores.NodeTypes, stores.PropertyDefs, stores.Relationships, stores.NodeDeleter)
	result, err := svc.Create(t.Context(), service.CreateInput{
		ParentID: parent, ScopeID: parent, NodeTypeKey: kind.TypeKey, Name: name, ActorID: actor,
		Props: map[string]json.RawMessage{kind.IncludedKey: mustJSON(content), kind.ExcludedKey: mustJSON("excluded")},
	})
	if err != nil || result == nil || result.View == nil {
		t.Fatalf("create production worker fixture: %v", err)
	}
	stored, err := stores.Views.Get(t.Context(), result.View.ID)
	if err != nil || stored == nil {
		t.Fatalf("read production worker fixture: %v", err)
	}
	for _, key := range []string{"parent_id", "scope_id"} {
		var id string
		if err := json.Unmarshal(stored.Props[key], &id); err != nil || id != parent.String() {
			t.Fatalf("production worker fixture %s does not match its entry point: %v", key, err)
		}
	}
	return stored.ID
}
