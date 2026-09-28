package integration

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// nativeAccess is the strict access object stored on every page.
type nativeAccess struct {
	Versions   []string `json:"versions"`
	Keys       []string `json:"keys"`
	Generation int      `json:"generation"`
}

// nativeDocument is one page document with the mapping's property names.
// A nil PageText omits the property.
type nativeDocument struct {
	NodeID            string       `json:"node_id"`
	NodeType          string       `json:"node_type"`
	NodeRevision      string       `json:"node_revision"`
	ProjectionVersion string       `json:"projection_version"`
	PageOrdinal       int          `json:"page_ordinal"`
	Name              string       `json:"name"`
	PageText          *string      `json:"page_text,omitempty"`
	Retired           bool         `json:"retired"`
	SearchGeneration  int          `json:"search_generation"`
	Access            nativeAccess `json:"access"`
}

func nativeDocumentFor(id, text string) nativeDocument {
	return nativeDocument{
		NodeID: "node-" + id, NodeType: "generic", NodeRevision: "revision-1",
		ProjectionVersion: "projection-1", PageOrdinal: 0, Name: id, PageText: &text,
		Retired: false, SearchGeneration: 1,
		Access: nativeAccess{Versions: []string{"org-scope-v1"}, Keys: []string{"opaque-key"}, Generation: 1},
	}
}

func nativePageDocument(t *testing.T, id, text string) []byte {
	t.Helper()
	return encodeNativeJSON(t, nativeDocumentFor(id, text))
}

// nativeJSON lists the request values the native tests encode.
type nativeJSON interface {
	nativeDocument | map[string]nativeBulkTarget | nativeAccessUpdateBody | map[string]json.RawMessage
}

func encodeNativeJSON[Value nativeJSON](t *testing.T, value Value) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type nativeBulkTarget struct {
	Index       string `json:"_index"`
	ID          string `json:"_id"`
	Version     int    `json:"version,omitempty"`
	VersionType string `json:"version_type,omitempty"`
}

// nativeIndexAction returns one bulk index action at an external version.
func nativeIndexAction(t *testing.T, index, id string, version int, document []byte) string {
	t.Helper()
	target := nativeBulkTarget{Index: index, ID: id, Version: version, VersionType: "external_gte"}
	header := encodeNativeJSON(t, map[string]nativeBulkTarget{"index": target})
	return string(header) + "\n" + string(document) + "\n"
}

type nativeAccessDocument struct {
	SearchGeneration int          `json:"generation"`
	Access           nativeAccess `json:"access"`
}

type nativeAccessUpdateScript struct {
	Source string               `json:"source"`
	Lang   string               `json:"lang"`
	Params nativeAccessDocument `json:"params"`
}

type nativeAccessUpdateBody struct {
	Script nativeAccessUpdateScript `json:"script"`
}

const (
	// nativeBulkRetryWindow bounds the resends of a bulk request that the ML
	// Commons memory circuit breaker rejects with status 429.
	nativeBulkRetryWindow = 2 * time.Minute
	// nativeBulkRetryInterval is the delay between those resends.
	nativeBulkRetryInterval = 2 * time.Second
)

// nativeBulk sends body through the typed bulk API with refresh and returns
// every item result in request order. It resends the whole request while any
// item returns status 429, as the production worker retries rejected work.
// Every action uses an external version, and a resend rewrites the same
// document version.
func nativeBulk(t *testing.T, client *opensearchapi.Client, body string) []opensearchapi.BulkRespItem {
	t.Helper()
	deadline := time.Now().Add(nativeBulkRetryWindow)
	for {
		items := sendNativeBulk(t, client, body)
		if !slices.ContainsFunc(items, func(item opensearchapi.BulkRespItem) bool { return item.Status == http.StatusTooManyRequests }) ||
			time.Now().After(deadline) {
			return items
		}
		time.Sleep(nativeBulkRetryInterval)
	}
}

func sendNativeBulk(t *testing.T, client *opensearchapi.Client, body string) []opensearchapi.BulkRespItem {
	t.Helper()
	response, err := client.Bulk(t.Context(), opensearchapi.BulkReq{
		Body: strings.NewReader(body), Params: opensearchapi.BulkParams{Refresh: "true"},
	})
	if response == nil || len(response.Items) == 0 {
		t.Fatalf("typed bulk returned no item results: %v", err)
	}
	items := make([]opensearchapi.BulkRespItem, 0, len(response.Items))
	for _, item := range response.Items {
		for _, result := range item {
			items = append(items, result)
		}
	}
	return items
}

func requireBulkSucceeded(t *testing.T, items []opensearchapi.BulkRespItem) {
	t.Helper()
	for number, item := range items {
		if item.Error != nil || item.Status >= 300 {
			t.Fatalf("bulk item %d for %s failed with status %d: %+v", number, item.ID, item.Status, item.Error)
		}
	}
}

// requireBulkRejected requires one item to fail with status and error type.
func requireBulkRejected(t *testing.T, items []opensearchapi.BulkRespItem, status int, errorType string) {
	t.Helper()
	if len(items) != 1 {
		t.Fatalf("bulk returned %d item results, want 1", len(items))
	}
	item := items[0]
	if item.Status != status || item.Error == nil || item.Error.Type != errorType {
		t.Fatalf("bulk item %s status %d error %+v, want status %d type %s", item.ID, item.Status, item.Error, status, errorType)
	}
}
