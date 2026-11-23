package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/domain/node"
)

func requireStoredSearchManifest(
	t *testing.T,
	stores *fdbadapter.Stores,
	manifest datagen.SearchManifest,
	entryID uuid.UUID,
) {
	t.Helper()
	if len(manifest.Types) != 3 || len(manifest.Properties) != 6 {
		t.Fatal("manifest omits created metadata identities")
	}

	for _, exported := range manifest.Types {
		stored, err := stores.NodeTypes.Get(t.Context(), manifest.OrgID, exported.ID)
		if err != nil || stored == nil || stored.TypeKey != exported.Key {
			t.Fatalf("read exported node type %s: %v", exported.ID, err)
		}
	}

	for _, exported := range manifest.Properties {
		stored, err := stores.PropertyDefs.Get(t.Context(), manifest.OrgID, exported.ID)
		if err != nil || stored == nil || stored.Name != exported.Key || stored.Search == nil ||
			stored.Search.Include != exported.Included {
			t.Fatalf("read exported projection %s: %v", exported.ID, err)
		}
	}

	byType := make(map[string]map[uuid.UUID]bool)

	for _, exported := range manifest.Nodes {
		stored, err := stores.Nodes.Get(t.Context(), manifest.OrgID, exported.ID)
		if err != nil || stored == nil {
			t.Fatalf("read exported node %s: %v", exported.ID, err)
		}

		if stored.Name != exported.Name || stored.NodeType != exported.NodeType {
			t.Fatalf("exported node %s differs from stored node", exported.ID)
		}

		var scopeID string
		scopeErr := json.Unmarshal(stored.Props["scope_id"], &scopeID)
		if string(stored.Props["parent_id"]) != string(jsonStr(entryID.String())) {
			t.Fatalf(
				"exported node %s has parent property %s",
				stored.ID,
				stored.Props["parent_id"],
			)
		}

		parents, parentErr := stores.Relationships.ListBySource(
			t.Context(),
			manifest.OrgID,
			stored.ID,
			node.RelChildOf,
		)
		_, included := stored.Props[exported.IncludedKey]
		_, excluded := stored.Props[exported.ExcludedKey]
		if len(stored.Props) != 4 || !included || !excluded || parentErr != nil || len(
			parents,
		) != 1 || parents[0].TargetID != entryID || scopeErr != nil || scopeID != entryID.String() {
			t.Fatalf(
				"exported node %s stores properties %v and parent edges %v, want %s, %s, scope_id %s, and one child_of edge to that scope; parent error %v, scope error %v",
				exported.ID,
				stored.Props,
				parents,
				exported.IncludedKey,
				exported.ExcludedKey,
				entryID,
				parentErr,
				scopeErr,
			)
		}

		var text string
		if err := json.Unmarshal(
			stored.Props[exported.IncludedKey],
			&text,
		); err != nil || len(text) != exported.TextBytes {
			t.Fatalf("exported text length for %s differs: %v", exported.ID, err)
		}

		if byType[stored.NodeType] == nil {
			byType[stored.NodeType] = make(map[uuid.UUID]bool)
		}

		if byType[stored.NodeType][exported.ID] {
			t.Fatalf("duplicate exported node %s", exported.ID)
		}

		byType[stored.NodeType][exported.ID] = true
	}

	if len(byType) != 3 {
		t.Fatalf("fixture uses %d types, want three isolated types", len(byType))
	}

	for position, testCase := range manifest.Cases {
		if position < 6 {
			if testCase.Match != "ranked_targets" || testCase.RankLimit != 25 || len(
				testCase.ExpectedIDs,
			) != 2 || len(byType[testCase.NodeType]) != 162 {
				t.Fatalf("semantic case differs from target ranking contract: %+v", testCase)
			}
		} else {
			if testCase.Match != "exact" || len(
				testCase.ExpectedIDs,
			) != len(byType[testCase.NodeType]) {
				t.Fatalf("exact case differs from isolated type: %+v", testCase)
			}
		}

		for _, nodeID := range testCase.ExpectedIDs {
			if !byType[testCase.NodeType][nodeID] {
				t.Fatalf("case %q exports node %s outside its type", testCase.Query, nodeID)
			}
		}
	}

	if len(manifest.Cases[6].ExpectedIDs) != 31 || len(manifest.Cases[7].ExpectedIDs) != 1 {
		t.Fatal("continuation and final-page counts differ from the declared workload")
	}

	t.Logf(
		"prepared nodes=%d semantic=%d continuation=%d final_page=%d with disabled public search",
		len(manifest.Nodes),
		162,
		31,
		1,
	)
}
