package integration

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	searchdomain "goodkind.io/tack/internal/domain/search"
)

func splitMapping(t *testing.T, fixture queryFixture, index string) string {
	t.Helper()
	response, err := fixture.Client.Indices.Mapping.Get(WithTestOperation(t, t.Context()), &opensearchapi.MappingGetReq{Indices: []string{index}})
	if err != nil {
		t.Fatalf("read split mapping: %v", err)
	}
	entry, found := response.GetIndices()[index]
	if !found {
		t.Fatal("split mapping is absent")
	}
	var mapping map[string]any
	if err := json.Unmarshal(entry.Mappings, &mapping); err != nil {
		t.Fatalf("decode split mapping: %v", err)
	}
	encoded, err := json.Marshal(mapping)
	if err != nil {
		t.Fatalf("encode split mapping: %v", err)
	}
	return string(encoded)
}

// fixedSearchFields are the top-level index fields that Tack defines, plus the
// chunk field that OpenSearch generates for page_text.
var fixedSearchFields = []string{
	"access", "name", "node_id", "node_revision", "node_type", "page_ordinal", "page_text",
	"page_text_semantic_info", "projection_version", "retired", "search_generation",
}

// mappedObject is one strict object of an index mapping and its field
// definitions.
type mappedObject struct {
	Dynamic    string                     `json:"dynamic"`
	Properties map[string]json.RawMessage `json:"properties"`
}

// requireFixedSearchMapping reads the mapping of index from OpenSearch and
// requires strict dynamic mapping with exactly the fixed top-level fields and
// the three access fields.
func requireFixedSearchMapping(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	response, err := client.Indices.Mapping.Get(WithTestOperation(t, t.Context()), &opensearchapi.MappingGetReq{Indices: []string{index}})
	if err != nil {
		t.Fatalf("read the mapping of index %s: %v", index, err)
	}
	entry, found := response.GetIndices()[index]
	if !found {
		t.Fatalf("the mapping of index %s is absent", index)
	}
	var mapping, access mappedObject
	if err := json.Unmarshal(entry.Mappings, &mapping); err != nil {
		t.Fatalf("decode the mapping of index %s: %v", index, err)
	}
	if err := json.Unmarshal(mapping.Properties["access"], &access); err != nil {
		t.Fatalf("decode the access mapping of index %s: %v", index, err)
	}
	fields, accessFields := slices.Sorted(maps.Keys(mapping.Properties)), slices.Sorted(maps.Keys(access.Properties))
	wantAccess := []string{"generation", "keys", "versions"}
	if mapping.Dynamic != "strict" || access.Dynamic != "strict" || !slices.Equal(fields, fixedSearchFields) || !slices.Equal(accessFields, wantAccess) {
		t.Fatalf("index %s mapping is %q with fields %v and access %q with fields %v, want strict with %v and %v",
			index, mapping.Dynamic, fields, access.Dynamic, accessFields, fixedSearchFields, wantAccess)
	}
}

func savedSparseNodes(t *testing.T, fixture queryFixture, query searchdomain.Query, tokens json.RawMessage) []uuid.UUID {
	t.Helper()
	ranker := fixture.Adapter.Ranker(search.RankerSettings{KeepAlive: time.Minute, TokenBytes: 65536, BatchSize: 100})
	created, err := fixture.Client.PointInTime.Create(WithTestOperation(t, t.Context()), opensearchapi.PointInTimeCreateReq{
		Indices: []string{query.Index}, Params: opensearchapi.PointInTimeCreateParams{KeepAlive: time.Minute},
	})
	if err != nil || created.PitID == "" || created.Shards.Failed != 0 {
		t.Fatalf("open saved sparse snapshot: %v, response=%+v", err, created)
	}
	snapshot := searchdomain.Snapshot{PITID: created.PitID, Index: query.Index, QueryTokens: bytes.Clone(tokens)}
	defer func() {
		if err := ranker.Close(WithTestOperation(t, t.Context()), snapshot); err != nil {
			t.Errorf("close saved sparse snapshot: %v", err)
		}
	}()
	// Empty lexical text selects only the saved sparse weights, without inference.
	query.Text = ""
	var nodes []uuid.UUID
	var after json.RawMessage
	for range 10_000 {
		batch, err := ranker.Read(WithTestOperation(t, t.Context()), query, snapshot, after)
		if err != nil {
			t.Fatalf("read saved sparse query: %v", err)
		}
		snapshot.PITID = batch.PITID
		if len(batch.Hits) == 0 {
			slices.SortFunc(nodes, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
			return slices.Compact(nodes)
		}
		for _, hit := range batch.Hits {
			nodes = append(nodes, hit.NodeID)
		}
		after = bytes.Clone(batch.Hits[len(batch.Hits)-1].Sort)
	}
	t.Fatal("saved sparse query did not exhaust")
	return nil
}
