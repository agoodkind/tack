package integration

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// strictMappingError is the OpenSearch error type for an undeclared property.
const strictMappingError = "strict_dynamic_mapping_exception"

// TestSearchNativeDocumentForms requires OpenSearch to return each page text
// form that Tack writes unchanged. The forms exclude an empty page because
// Tack rejects an empty page before any write.
func TestSearchNativeDocumentForms(t *testing.T) {
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(t.Context())
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
	model, err := adapter.Provision(t.Context())
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

func nativeSource(t *testing.T, client *opensearchapi.Client, index, id string) map[string]json.RawMessage {
	t.Helper()
	response, err := client.Document.Get(t.Context(), opensearchapi.DocumentGetReq{Index: index, DocumentID: id})
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
