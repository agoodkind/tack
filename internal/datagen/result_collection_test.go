package datagen

import "testing"

func TestResultParsesCollectionReferencesAndNodeFields(t *testing.T) {
	t.Parallel()
	result := Result{Content: []ToolContent{{
		Type: "text",
		Text: "#### States\n\n2 states found.\n" +
			"- `QA::Todo`\n" +
			"  - Name: Todo\n" +
			"  - Type: `state`\n" +
			"- `QA::Done`\n" +
			"  - Name: Done\n" +
			"  - Type: `state`\n",
	}}}
	items := result.collectionItems()
	if len(items) != 2 {
		t.Fatalf("collectionItems() len = %d", len(items))
	}
	if items[0].Reference != "QA::Todo" || items[1].Name != "Done" {
		t.Fatalf("collectionItems() = %#v", items)
	}

	node := Result{Content: []ToolContent{{
		Type: "text",
		Text: "#### Todo\n\n- Group: unstarted\n" +
			"- Sort order: 10\n" +
			"- Raw id: `0196fca8-1c28-7b6e-987a-b08b165dbf30`\n",
	}}}
	if node.field("Group") != "unstarted" || node.intField("Sort order") != 10 {
		t.Fatalf("node fields = %q, %d", node.field("Group"), node.intField("Sort order"))
	}
	if items := node.collectionItems(); len(items) != 0 {
		t.Fatalf("node collectionItems() = %#v, want none", items)
	}
}

func TestCollectionItemsParseProductionReferenceStrategies(t *testing.T) {
	const rawID = "0196fca8-1c28-7b6e-987a-b08b165dbf30"
	tests := []struct {
		plural    string
		reference string
		strategy  soakReferenceStrategy
	}{
		{plural: "workspaces", reference: "qa-777-o01-w01", strategy: soakReferenceDirectProperty},
		{plural: "projects", reference: "Q0101", strategy: soakReferenceDirectProperty},
		{plural: "issues", reference: "Q0101-1", strategy: soakReferenceScopedSequence},
		{plural: "epics", reference: "Q0101-2", strategy: soakReferenceScopedSequence},
		{plural: "cycles", reference: "Q0101-3", strategy: soakReferenceScopedSequence},
		{plural: "modules", reference: "Q0101-4", strategy: soakReferenceScopedSequence},
		{plural: "states", reference: "Q0101::Todo", strategy: soakReferenceNestedScopedProperty},
		{plural: "labels", reference: "qa-777-o01-w01::security", strategy: soakReferenceWorkspaceScopedProperty},
		{plural: "comments", reference: rawID, strategy: soakReferenceUUIDOnly},
		{plural: "activities", reference: rawID, strategy: soakReferenceUUIDOnly},
	}
	for _, test := range tests {
		t.Run(test.plural, func(t *testing.T) {
			result := Result{Content: []ToolContent{{
				Type: "text",
				Text: discoveryListText(test.plural, test.reference, "Node name"),
			}}}
			items := result.collectionItems()
			if len(items) != 1 {
				t.Fatalf("collectionItems() len = %d", len(items))
			}
			item := items[0]
			if item.Reference != test.reference || item.NodeType != discoveryNodeType(test.plural) {
				t.Fatalf("collectionItems() = %#v", item)
			}
			strategy, err := soakNodeReferenceStrategy(item.NodeType)
			if err != nil || strategy != test.strategy {
				t.Fatalf("strategy = %q, %v", strategy, err)
			}
		})
	}
}
