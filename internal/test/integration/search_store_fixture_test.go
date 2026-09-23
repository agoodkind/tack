package integration

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/testenv"
)

// searchFixtureName is the name of every search fixture node.
const searchFixtureName = "Search fixture"

// newSearchStore creates isolated search stores over the real test FoundationDB.
func newSearchStore(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	cluster := testenv.FoundationDB(t)
	prefix := append([]byte("search-test:"), uuid.Must(uuid.NewV7()).String()...)
	prefix = append(prefix, ':')
	fdbadapter.SetTestPrefix(prefix)
	stores, err := fdbadapter.NewStores(cluster, testTransactionTimeout, nil)
	if err != nil {
		fdbadapter.SetTestPrefix(nil)
		t.Fatalf("open search stores: %v", err)
	}
	t.Cleanup(func() {
		clearPrefix(t, cluster, prefix)
		fdbadapter.SetTestPrefix(nil)
	})
	stores.EnableSearchWork()
	return stores
}

// reopenSearchStore opens a second connection over the same cluster and
// test prefix, as a restarted process would.
func reopenSearchStore(t *testing.T) *fdbadapter.Stores {
	t.Helper()
	stores, err := fdbadapter.NewStores(testenv.FoundationDB(t), testTransactionTimeout, nil)
	if err != nil {
		t.Fatalf("reopen search stores: %v", err)
	}
	stores.EnableSearchWork()
	return stores
}

// searchFixture identifies one node and its opaque generated metadata keys.
type searchFixture struct {
	OrgID       uuid.UUID
	NodeID      uuid.UUID
	TypeKey     string
	IncludedKey string
	ExcludedKey string
}

func opaqueSearchKey(prefix string) string {
	return prefix + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

// putSearchText stores an entry-point node type with one included and one
// excluded property of unfamiliar opaque types, then writes one node with
// both values in a new organization.
func putSearchText(t *testing.T, stores *fdbadapter.Stores, included, excluded string) searchFixture {
	t.Helper()
	return putSearchTextInOrg(t, stores, uuid.Must(uuid.NewV7()), included, excluded)
}

func putSearchTextInOrg(t *testing.T, stores *fdbadapter.Stores, orgID uuid.UUID, included, excluded string) searchFixture {
	t.Helper()
	fixture := searchFixture{
		OrgID: orgID, NodeID: uuid.Must(uuid.NewV7()), TypeKey: opaqueSearchKey("n"),
		IncludedKey: opaqueSearchKey("p"), ExcludedKey: opaqueSearchKey("p"),
	}
	definitions := []*node.PropertyDef{
		{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: fixture.IncludedKey, Type: node.PropertyType(opaqueSearchKey("t")),
			Search: &node.SearchProjection{Include: true, Order: 0, Rule: node.TextRule{Mode: node.TextRuleScalar}},
		},
		{
			ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: fixture.ExcludedKey, Type: node.PropertyType(opaqueSearchKey("t")),
			Search: &node.SearchProjection{Include: false, Order: 1, Rule: node.TextRule{Mode: node.TextRuleScalar}},
		},
	}
	for _, definition := range definitions {
		if err := stores.PropertyDefs.Set(t.Context(), definition); err != nil {
			t.Fatalf("store property definition: %v", err)
		}
	}
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: fixture.TypeKey, TypeKey: fixture.TypeKey,
		Slug: fixture.TypeKey, PluralSlug: fixture.TypeKey + "s",
		PropertyDefIDs: []uuid.UUID{definitions[0].ID, definitions[1].ID}, Features: node.Features{node.FeatureIsEntryPoint},
	}
	if err := stores.NodeTypes.Set(t.Context(), kind); err != nil {
		t.Fatalf("store node type: %v", err)
	}
	writeSearchNode(t, stores, fixture, included, excluded)
	return fixture
}

// writeSearchNode stores the fixture node with the included and excluded values.
func writeSearchNode(t *testing.T, stores *fdbadapter.Stores, fixture searchFixture, included, excluded string) {
	t.Helper()
	props := map[string]json.RawMessage{
		fixture.IncludedKey: mustJSON(included),
		fixture.ExcludedKey: mustJSON(excluded),
	}
	now := clock.Now().UTC()
	value := &node.Node{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: fixture.NodeID, OrgID: fixture.OrgID, NodeType: fixture.TypeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.Set(t.Context(), value, view); err != nil {
		t.Fatalf("store search node: %v", err)
	}
}
