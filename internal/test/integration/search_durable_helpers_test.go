package integration_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/test/integration"
)

// durableDocumentLimit bounds the documents of one node that a test reads.
const durableDocumentLimit = 1000

// durableDocument is one page document of a node in the serving index.
type durableDocument struct {
	Revision string  `json:"node_revision"`
	Ordinal  uint64  `json:"page_ordinal"`
	Text     *string `json:"page_text"`
}

// durableNodeDocuments refreshes the serving index of fixture and returns
// the documents of nodeID with the requested retirement state, in page
// order.
func durableNodeDocuments(t *testing.T, fixture integration.QueryFixture, nodeID uuid.UUID, retired bool) []durableDocument {
	t.Helper()
	refresh := &opensearchapi.IndicesRefreshReq{Index: []string{fixture.Index}}
	if _, err := fixture.Client.Indices.Refresh(integration.WithTestOperation(t, t.Context()), refresh); err != nil {
		t.Fatalf("refresh index %s: %v", fixture.Index, err)
	}
	query := fmt.Sprintf(`{"size":%d,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":%t}}]}},"sort":[{"page_ordinal":"asc"}]}`,
		durableDocumentLimit, nodeID.String(), retired)
	request := &opensearchapi.SearchReq{Indices: []string{fixture.Index}, Body: strings.NewReader(query)}
	response, err := fixture.Client.Search(integration.WithTestOperation(t, t.Context()), request)
	if err != nil {
		t.Fatalf("search the documents of node %s: %v", nodeID, err)
	}
	documents := make([]durableDocument, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		var document durableDocument
		if err := json.Unmarshal(hit.Source, &document); err != nil {
			t.Fatalf("decode document %s of node %s: %v", hit.ID, nodeID, err)
		}
		documents = append(documents, document)
	}
	return documents
}

// editDurableNode replaces the included text of nodeID through the node
// store, which schedules the search work of the edit.
func editDurableNode(t *testing.T, fixture integration.QueryFixture, kind integration.OpaqueKind, nodeID uuid.UUID, included string) {
	t.Helper()
	current, err := fixture.Stores.Nodes.Get(integration.WithTestOperation(t, t.Context()), kind.OrgID, nodeID)
	if err != nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}
	encoded, err := json.Marshal(included)
	if err != nil {
		t.Fatalf("encode the text of node %s: %v", nodeID, err)
	}
	props := maps.Clone(current.Props)
	props[kind.IncludedKey] = encoded
	current.Props = props
	current.UpdatedAt = clock.Now().UTC()
	view := &node.NodeView{
		ID: current.ID, OrgID: current.OrgID, NodeType: current.NodeType, Name: current.Name,
		Props: props, CreatedAt: current.CreatedAt, UpdatedAt: current.UpdatedAt,
	}
	if err := fixture.Stores.Nodes.Set(integration.WithTestOperation(t, t.Context()), current, view); err != nil {
		t.Fatalf("edit node %s: %v", nodeID, err)
	}
}
