package integration_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// failureQuery is the included text of every node in the failure corpus.
	failureQuery = "saffron quarry beacon"
	// failureNodes exceeds the 25 results of one page. The first page returns
	// a cursor.
	failureNodes = 30
	// snapshotEndedText is part of the tack_search error for a point in time
	// that OpenSearch no longer recognizes.
	snapshotEndedText = "the search snapshot ended"
	// expiredCursorText is part of the tack_search error for a closed session.
	expiredCursorText = "the search cursor expired"
	// unexpectedErrorText is part of the tack_search error for a storage or
	// index failure.
	unexpectedErrorText = "Unexpected server error"
	// foreignTokenizerDigest is a tokenizer SHA-256 that no pinned model has.
	foreignTokenizerDigest = "0000000000000000000000000000000000000000000000000000000000000000"
)

// failureMappingMeta is the _meta object of the serving index mapping.
type failureMappingMeta struct {
	MappingVersion  string `json:"mapping_version"`
	ModelID         string `json:"model_id"`
	TokenizerSHA256 string `json:"tokenizer_sha256"`
}

// TestSearchQueryFailuresReturnExplicitErrors requires an explicit tack_search
// error result for three failures. The test deletes the point in time of an
// open session, and the continuation must report the ended snapshot. A retry
// of that cursor must report an expired cursor. The test then replaces the
// resolve record of one matching node with invalid JSON, and a new search
// must fail its FoundationDB summary read. After the test restores the
// record, the test sets a foreign tokenizer digest in the serving index
// mapping, and a new search must fail its mapping check.
func TestSearchQueryFailuresReturnExplicitErrors(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	workspace := fixture.Workspaces[0]
	kind := integration.PutOpaqueKind(t, fixture, workspace.OrgID)
	entryID := integration.EntryPoint(t, fixture, workspace)
	corrupted := integration.PutOpaqueNode(t, fixture, kind, entryID, "Failure node 0", failureQuery, "excluded")
	for number := 1; number < failureNodes; number++ {
		integration.PutOpaqueNode(t, fixture, kind, entryID, fmt.Sprintf("Failure node %d", number), failureQuery, "excluded")
	}
	integration.DrainSearchWork(t, fixture.Worker, 10*failureNodes+100)

	first := integration.CallSearch(t, fixture.Harness, failureQuery, "")
	session := integration.CursorSession(t, fixture, first.Cursor)
	deletePointInTime(t, fixture, session.Snapshot.PITID)
	requireSearchError(t, fixture.Harness, first.Cursor, snapshotEndedText)
	requireSearchError(t, fixture.Harness, first.Cursor, expiredCursorText)

	resolveKey := integration.NodeResolveRawKey(corrupted)
	resolve := integration.ReplaceRawValue(t, resolveKey, []byte("{"))
	requireSearchError(t, fixture.Harness, "", unexpectedErrorText)
	integration.ReplaceRawValue(t, resolveKey, resolve)
	integration.CallSearch(t, fixture.Harness, failureQuery, "")

	info, err := fixture.Adapter.IndexInfo(integration.WithTestOperation(t, t.Context()), fixture.Index)
	if err != nil {
		t.Fatalf("read serving index mapping metadata: %v", err)
	}
	putMappingMeta(t, fixture, failureMappingMeta{MappingVersion: info.MappingVersion, ModelID: info.ModelID, TokenizerSHA256: foreignTokenizerDigest})
	requireSearchError(t, fixture.Harness, "", unexpectedErrorText)
}

// requireSearchError calls tack_search for failureQuery and requires an error
// result that contains want.
func requireSearchError(t *testing.T, harness *integration.MCPHarness, cursor, want string) {
	t.Helper()
	page, err := integration.TrySearch(harness, failureQuery, cursor)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("tack_search returned %d results and error %v, want an error result containing %q", len(page.IDs), err, want)
	}
}

// deletePointInTime deletes one point in time through the OpenSearch client.
func deletePointInTime(t *testing.T, fixture integration.QueryFixture, pitID string) {
	t.Helper()
	id, err := opensearchapi.ParsePITID(pitID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fixture.Client.PointInTime.Delete(integration.WithTestOperation(t, t.Context()), &opensearchapi.DeletePITReq{
		Body: &opensearchapi.DeletePITBody{PITID: []opensearchapi.PITID{id}}, Header: nil, Params: &opensearchapi.DeletePITParams{},
	})
	if err != nil {
		t.Fatalf("delete point in time of the open session: %v", err)
	}
}

// putMappingMeta replaces the _meta object of the serving index mapping.
func putMappingMeta(t *testing.T, fixture integration.QueryFixture, meta failureMappingMeta) {
	t.Helper()
	body, err := json.Marshal(map[string]failureMappingMeta{"_meta": meta})
	if err != nil {
		t.Fatalf("encode mapping metadata: %v", err)
	}
	_, err = fixture.Client.Indices.PutMapping(integration.WithTestOperation(t, t.Context()), &opensearchapi.IndicesPutMappingReq{
		Indices: []string{fixture.Index}, BodyReader: strings.NewReader(string(body)), Header: nil, Params: &opensearchapi.IndicesPutMappingParams{},
	})
	if err != nil {
		t.Fatalf("replace mapping metadata of %s: %v", fixture.Index, err)
	}
}
