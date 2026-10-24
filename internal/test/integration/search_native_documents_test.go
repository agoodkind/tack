package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/domain/node"
)

// strictMappingError is the OpenSearch error type for an undeclared property.
const strictMappingError = "strict_dynamic_mapping_exception"

// TestSearchNativeDocumentForms requires OpenSearch to return each page text
// form that Tack writes unchanged. The forms exclude an empty page because
// Tack rejects an empty page before any write.
func TestSearchNativeDocumentForms(t *testing.T) {
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	const index = "native-document-forms"
	createNativeSearchIndex(t, adapter, client, model, index)
	forms := []struct {
		name string
		text string
	}{
		{name: "ordinary", text: "Searchable ordinary page"},
		{name: "unicode", text: unicodePage4096()},
		{name: "newline", text: "\n\n\n"},
	}
	for _, form := range forms {
		t.Run(form.name, func(t *testing.T) {
			requireBulkSucceeded(t, nativeBulk(t, client, nativeIndexAction(t, index, form.name, 1, nativePageDocument(t, form.name, form.text))))
			source := nativeSource(t, client, index, form.name)
			var pageText string
			if err := json.Unmarshal(source["page_text"], &pageText); err != nil {
				t.Fatal(err)
			}
			if pageText != form.text {
				t.Fatalf("stored page text = %q, want %q", pageText, form.text)
			}
			if len(form.text) == len(unicodePage4096()) {
				requireNativeChunks(t, source["page_text_semantic_info"], form.text)
			}
		})
	}
}

func TestSearchNativeStrictMapping(t *testing.T) {
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	const index = "native-strict-mapping"
	createNativeSearchIndex(t, adapter, client, model, index)
	var document map[string]json.RawMessage
	if err := json.Unmarshal(nativePageDocument(t, "unknown", "hello"), &document); err != nil {
		t.Fatal(err)
	}
	topLevel := map[string]json.RawMessage{"unexpected": json.RawMessage("true")}
	for key, value := range document {
		topLevel[key] = value
	}
	items := nativeBulk(t, client, nativeIndexAction(t, index, "unknown-top-level", 1, encodeNativeJSON(t, topLevel)))
	requireBulkRejected(t, items, http.StatusBadRequest, strictMappingError)

	var access map[string]json.RawMessage
	if err := json.Unmarshal(document["access"], &access); err != nil {
		t.Fatal(err)
	}
	access["unexpected"] = json.RawMessage(`"permission-type"`)
	document["access"] = encodeNativeJSON(t, access)
	items = nativeBulk(t, client, nativeIndexAction(t, index, "unknown-access", 1, encodeNativeJSON(t, document)))
	requireBulkRejected(t, items, http.StatusBadRequest, strictMappingError)
}

// opaqueMappingProperties is the number of opaque property definitions on the
// node that TestSearchManyOpaquePropertiesKeepFixedMapping indexes.
const opaqueMappingProperties = 200

// TestSearchManyOpaquePropertiesKeepFixedMapping indexes one node with many
// opaque scalar and structured properties through the production worker. It
// then reads the index mapping from OpenSearch and requires the fixed
// top-level and access fields only.
func TestSearchManyOpaquePropertiesKeepFixedMapping(t *testing.T) {
	stores := newSearchStore(t)
	adapter, client, _, index := newSearchIndex(t, stores)
	orgID, nodeID, typeKey := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), opaqueSearchKey("n")
	kind := &node.NodeType{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: typeKey, TypeKey: typeKey, Slug: typeKey,
		PluralSlug: typeKey + "s", Features: node.Features{node.FeatureIsEntryPoint},
	}
	props := make(map[string]json.RawMessage, opaqueMappingProperties)
	for number := range opaqueMappingProperties {
		definition, value := opaqueMappingDefinition(orgID, number)
		if err := stores.PropertyDefs.Set(WithTestOperation(t, t.Context()), definition); err != nil {
			t.Fatalf("store opaque property definition %d: %v", number, err)
		}
		kind.PropertyDefIDs = append(kind.PropertyDefIDs, definition.ID)
		props[definition.Name] = value
	}
	if err := stores.NodeTypes.Set(WithTestOperation(t, t.Context()), kind); err != nil {
		t.Fatalf("store node type %s: %v", typeKey, err)
	}
	now := clock.Now().UTC()
	value := &node.Node{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	view := &node.NodeView{ID: nodeID, OrgID: orgID, NodeType: typeKey, Name: searchFixtureName, Props: props, CreatedAt: now, UpdatedAt: now}
	if err := stores.Nodes.Set(WithTestOperation(t, t.Context()), value, view); err != nil {
		t.Fatalf("store node %s: %v", nodeID, err)
	}
	runSearchWorkerUntilIdle(t, newSearchWorker(t, stores, adapter, clock.Wall{}, searchWorkerSettings(4096)))
	var indexed strings.Builder
	for _, page := range searchNodePages(t, client, index, nodeID, false) {
		if page.PageText != nil {
			indexed.WriteString(*page.PageText)
		}
	}
	last := fmt.Sprintf("opaque value %d\n", opaqueMappingProperties-1)
	if !strings.Contains(indexed.String(), "opaque value 0\n") || !strings.Contains(indexed.String(), last) {
		t.Fatalf("indexed pages of node %s omit the first or last opaque value:\n%s", nodeID, indexed.String())
	}
	requireFixedSearchMapping(t, client, index)
}

// opaqueMappingDefinition returns an included definition with opaque names in
// organization orgID and the node value for it. Odd numbers use a structured
// value with one opaque field key.
func opaqueMappingDefinition(orgID uuid.UUID, number int) (*node.PropertyDef, json.RawMessage) {
	scalar := node.TextRule{Mode: node.TextRuleScalar}
	text := fmt.Sprintf("opaque value %d", number)
	definition := &node.PropertyDef{
		ID: uuid.Must(uuid.NewV7()), OrgID: orgID, Name: opaqueSearchKey("p"), Type: node.PropertyType(opaqueSearchKey("t")),
		Search: &node.SearchProjection{Include: true, Order: number, Rule: scalar},
	}
	if number%2 == 0 {
		return definition, mustJSON(text)
	}
	field := opaqueSearchKey("f")
	definition.Search.Rule = node.TextRule{Mode: node.TextRuleObject, Fields: []node.TextField{{Key: field, Rule: scalar}}}
	return definition, mustJSON(map[string]string{field: text})
}

func nativeSource(t *testing.T, client *opensearchapi.Client, index, id string) map[string]json.RawMessage {
	t.Helper()
	response, err := client.Document.Get(WithTestOperation(t, t.Context()), opensearchapi.DocumentGetReq{Index: index, DocumentID: id})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Found {
		t.Fatalf("OpenSearch document %s was not found", id)
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(response.Source, &source); err != nil {
		t.Fatal(err)
	}
	return source
}
