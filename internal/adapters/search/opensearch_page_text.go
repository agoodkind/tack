package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// nodePageTextLimit bounds the active pages one NodePageText call reads.
const nodePageTextLimit = 1000

// pageOrdinalSort orders the active pages of one node by page ordinal.
var pageOrdinalSort = json.RawMessage(`[{"page_ordinal":{"order":"asc"}}]`)

type pageTextRequest struct {
	Size   int             `json:"size"`
	Source []string        `json:"_source"`
	Query  pageTextQuery   `json:"query"`
	Sort   json.RawMessage `json:"sort"`
}

type pageTextQuery struct {
	Bool pageTextFilter `json:"bool"`
}

type pageTextFilter struct {
	Filter []json.RawMessage `json:"filter"`
}

type pageTextSource struct {
	PageText string `json:"page_text"`
}

// NodePageText returns the stored page_text of every active page of nodeID
// in index, in page order. It reads only documents that the last refresh of
// index made searchable. A node with more than nodePageTextLimit active pages
// returns an error.
func (a *Adapter) NodePageText(ctx context.Context, index string, nodeID uuid.UUID) ([]string, error) {
	body, err := encodePageTextRequest(ctx, index, nodeID)
	if err != nil {
		return nil, err
	}
	response, err := a.api.Search(ctx, &opensearchapi.SearchReq{
		Indices: []string{index}, Body: bytes.NewReader(body), Header: nil, Params: opensearchapi.SearchParams{},
	})
	if err != nil {
		return nil, engineFailure(ctx, "search.page_text.read_failed", "read page text of node "+nodeID.String(), index, err)
	}
	if response.Timeout || response.Shards.Failed > 0 {
		incomplete := fmt.Errorf("search timed out %t with %d failed shards", response.Timeout, response.Shards.Failed)
		return nil, engineFailure(ctx, "search.page_text.read_incomplete", "read page text of node "+nodeID.String(), index, incomplete)
	}
	if len(response.Hits.Hits) > nodePageTextLimit {
		excess := fmt.Errorf("node has more than %d active pages", nodePageTextLimit)
		return nil, engineFailure(ctx, "search.page_text.too_many_pages", "read page text of node "+nodeID.String(), index, excess)
	}
	texts := make([]string, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		var source pageTextSource
		if err := json.Unmarshal(hit.Source, &source); err != nil {
			return nil, engineFailure(ctx, "search.page_text.decode_failed", "decode page "+hit.ID+" of node "+nodeID.String(), index, err)
		}
		texts = append(texts, source.PageText)
	}
	return texts, nil
}

// encodePageTextRequest builds a filter-only search for the active pages of
// nodeID. It asks for one page above the limit to detect an excess.
func encodePageTextRequest(ctx context.Context, index string, nodeID uuid.UUID) ([]byte, error) {
	const event = "search.page_text.encode_failed"
	nodeFilter, err := json.Marshal(termClause{Term: map[string]string{"node_id": nodeID.String()}})
	if err != nil {
		return nil, engineFailure(ctx, event, "encode node filter for "+nodeID.String(), index, err)
	}
	active, err := json.Marshal(booleanTermClause{Term: map[string]bool{"retired": false}})
	if err != nil {
		return nil, engineFailure(ctx, event, "encode retirement filter for "+nodeID.String(), index, err)
	}
	encoded, err := json.Marshal(pageTextRequest{
		Size: nodePageTextLimit + 1, Source: []string{"page_text"},
		Query: pageTextQuery{Bool: pageTextFilter{Filter: []json.RawMessage{nodeFilter, active}}},
		Sort:  pageOrdinalSort,
	})
	if err != nil {
		return nil, engineFailure(ctx, event, "encode page text request for "+nodeID.String(), index, err)
	}
	return encoded, nil
}
