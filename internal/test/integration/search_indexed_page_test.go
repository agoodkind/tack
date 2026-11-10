package integration_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
)

const (
	// indexedGenerationField stores the page field name search_generation,
	// which access updates change.
	indexedGenerationField = "search_generation"
	// indexedAccessField stores the page field name access, which access
	// updates change.
	indexedAccessField = "access"
)

// indexedPage stores one active OpenSearch page with its document ID, node
// ID, _seq_no, _primary_term, and raw JSON bytes for every field directly
// under _source.
type indexedPage struct {
	id          string
	nodeID      uuid.UUID
	seqNo       int64
	primaryTerm int64
	fields      map[string]json.RawMessage
}

// indexedAccess stores the decoded versions, keys, and generation from a
// page's access field.
type indexedAccess struct {
	Versions   []string `json:"versions"`
	Keys       []string `json:"keys"`
	Generation int64    `json:"generation"`
}

// indexedPages refreshes the index and returns each node's active pages in
// node and page order, including _seq_no and _primary_term. It fails the test
// when a node does not have an active page.
func indexedPages(t *testing.T, fixture rolloutFixture, nodes []uuid.UUID) []indexedPage {
	t.Helper()
	if _, err := fixture.client.Indices.Refresh(rolloutContext(t), &opensearchapi.IndicesRefreshReq{Indices: []string{fixture.index}}); err != nil {
		t.Fatalf("cannot refresh index %s: %v", fixture.index, err)
	}
	pages := make([]indexedPage, 0, len(nodes)*2)
	for _, nodeID := range nodes {
		query := fmt.Sprintf(`{"size":1000,"seq_no_primary_term":true,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":false}}]}},"sort":[{"page_ordinal":"asc"}]}`, nodeID.String())
		response, err := fixture.client.Search(rolloutContext(t), &opensearchapi.SearchReq{Indices: []string{fixture.index}, BodyReader: strings.NewReader(query)})
		if err != nil {
			t.Fatalf("cannot search pages of node %s: %v", nodeID, err)
		}
		if len(response.Hits.Hits) == 0 {
			t.Fatalf("node %s does not have active pages", nodeID)
		}
		for _, hit := range response.Hits.Hits {
			fields := map[string]json.RawMessage{}
			if err := json.Unmarshal(hit.Source, &fields); err != nil || hit.ID == nil || hit.SeqNo == nil || hit.PrimaryTerm == nil {
				t.Fatalf("cannot decode page %v of node %s: %v", hit.ID, nodeID, err)
			}
			pages = append(pages, indexedPage{id: *hit.ID, nodeID: nodeID, seqNo: *hit.SeqNo, primaryTerm: *hit.PrimaryTerm, fields: fields})
		}
	}
	return pages
}

// requireIndexedPagesUnwritten requires identical document IDs in the same
// order, identical _seq_no and _primary_term values, and identical bytes for
// every _source field. OpenSearch increases _seq_no on every document write.
func requireIndexedPagesUnwritten(t *testing.T, before, after []indexedPage) {
	t.Helper()
	requireSamePageIDs(t, before, after)
	for position, page := range after {
		previous := before[position]
		if page.seqNo != previous.seqNo || page.primaryTerm != previous.primaryTerm {
			t.Fatalf("page %s changed sequence number from %d to %d", page.id, previous.seqNo, page.seqNo)
		}
		if field := changedField(previous.fields, page.fields); field != "" {
			t.Fatalf("page %s changed field %s", page.id, field)
		}
	}
}

// requireAccessOnlyChange requires identical document IDs, different access
// bytes, a higher search_generation, and identical bytes for every other
// _source field.
func requireAccessOnlyChange(t *testing.T, before, after []indexedPage) {
	t.Helper()
	requireSamePageIDs(t, before, after)
	for position, page := range after {
		previous := before[position]
		if bytes.Equal(page.fields[indexedAccessField], previous.fields[indexedAccessField]) {
			t.Fatalf("page %s kept access %s", page.id, page.fields[indexedAccessField])
		}
		if pageGenerationOf(t, page) <= pageGenerationOf(t, previous) {
			t.Fatalf("page %s kept search generation %s", page.id, page.fields[indexedGenerationField])
		}
		current, earlier := maps.Clone(page.fields), maps.Clone(previous.fields)
		for _, field := range []string{indexedGenerationField, indexedAccessField} {
			delete(current, field)
			delete(earlier, field)
		}
		if field := changedField(earlier, current); field != "" {
			t.Fatalf("page %s changed field %s", page.id, field)
		}
	}
}

// requireSamePageIDs requires identical document IDs in the same order.
func requireSamePageIDs(t *testing.T, before, after []indexedPage) {
	t.Helper()
	if len(before) != len(after) {
		t.Fatalf("active page count changed from %d to %d", len(before), len(after))
	}
	for position, page := range after {
		if page.id != before[position].id {
			t.Fatalf("page %d changed document ID from %s to %s", position, before[position].id, page.id)
		}
	}
}

// changedField returns the first field in sorted order that either map omits
// or stores with different bytes. It returns an empty string when the maps
// match.
func changedField(before, after map[string]json.RawMessage) string {
	names := slices.Sorted(maps.Keys(before))
	for name := range after {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, name := range names {
		previous, found := before[name]
		current, present := after[name]
		if found != present || !bytes.Equal(previous, current) {
			return name
		}
	}
	return ""
}

// pageGenerationOf decodes search_generation as a number.
func pageGenerationOf(t *testing.T, page indexedPage) int64 {
	t.Helper()
	var generation json.Number
	if err := json.Unmarshal(page.fields[indexedGenerationField], &generation); err != nil {
		t.Fatalf("cannot decode the search generation of page %s: %v", page.id, err)
	}
	value, err := generation.Int64()
	if err != nil {
		t.Fatalf("cannot parse the search generation %q of page %s: %v", generation, page.id, err)
	}
	return value
}

// pageAccessOf decodes a page's access field.
func pageAccessOf(t *testing.T, page indexedPage) indexedAccess {
	t.Helper()
	access := indexedAccess{Versions: nil, Keys: nil, Generation: 0}
	if err := json.Unmarshal(page.fields[indexedAccessField], &access); err != nil {
		t.Fatalf("cannot decode the access of page %s: %v", page.id, err)
	}
	return access
}
