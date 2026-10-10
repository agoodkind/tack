package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/google/uuid"
	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// rankSortValues is the number of sort values in every page match.
const rankSortValues = 3

// rankResponse decodes the replacement point-in-time ID, the timeout and
// shard failure status, and each match's node ID and raw sort values from
// one OpenSearch search response.
type rankResponse struct {
	PITID    string `json:"pit_id"`
	TimedOut bool   `json:"timed_out"`
	Shards   struct {
		Failed int `json:"failed"`
	} `json:"_shards"`
	Hits struct {
		Hits []struct {
			Source struct {
				NodeID string `json:"node_id"`
			} `json:"_source"`
			Sort json.RawMessage `json:"sort"`
		} `json:"hits"`
	} `json:"hits"`
}

// Read returns the next raw batch after the exact sort position. An empty
// batch is the only engine exhaustion signal.
func (r *QueryRanker) Read(ctx context.Context, query searchdomain.Query, snapshot searchdomain.Snapshot, after json.RawMessage) (searchdomain.RankBatch, error) {
	var none searchdomain.RankBatch
	body, err := encodeRankRequest(query, snapshot, after, r.settings.BatchSize, r.settings.KeepAlive)
	if err != nil {
		return none, rankFailure(ctx, snapshot.Index, err)
	}
	partial := false
	request := opensearchapi.SearchReq{
		Indices: nil, Body: bytes.NewReader(body), Header: nil,
		Params: opensearchapi.SearchParams{AllowPartialSearchResults: &partial},
	}
	var decoded rankResponse
	response, err := opensearch.Do(ctx, r.adapter.client, http.MethodPost, request, &decoded)
	if err != nil {
		return none, rankFailure(ctx, snapshot.Index, fmt.Errorf("send search request: %w", engineCause(response, err)))
	}
	if response == nil {
		return none, rankFailure(ctx, snapshot.Index, errors.New("OpenSearch returned no search response"))
	}
	if response.IsError() {
		return none, r.searchError(ctx, snapshot, response)
	}
	if decoded.TimedOut || decoded.Shards.Failed > 0 {
		return none, rankFailure(ctx, snapshot.Index, fmt.Errorf("search timed out %t with %d failed shards", decoded.TimedOut, decoded.Shards.Failed))
	}
	batch := searchdomain.RankBatch{Hits: make([]searchdomain.RankHit, 0, len(decoded.Hits.Hits)), PITID: snapshot.PITID}
	if decoded.PITID != "" {
		batch.PITID = decoded.PITID
	}
	for _, hit := range decoded.Hits.Hits {
		nodeID, err := uuid.Parse(hit.Source.NodeID)
		if err != nil {
			return none, rankFailure(ctx, snapshot.Index, fmt.Errorf("decode page match node ID %q: %w", hit.Source.NodeID, err))
		}
		var values []json.RawMessage
		if err := json.Unmarshal(hit.Sort, &values); err != nil || len(values) != rankSortValues {
			return none, rankFailure(ctx, snapshot.Index, fmt.Errorf("page match for node %s has invalid sort values %s", nodeID, hit.Sort))
		}
		batch.Hits = append(batch.Hits, searchdomain.RankHit{NodeID: nodeID, Sort: hit.Sort})
	}
	return batch, nil
}

// searchError returns [searchdomain.ErrSnapshotLost] when OpenSearch no
// longer recognizes the point in time and an explicit error otherwise.
func (r *QueryRanker) searchError(ctx context.Context, snapshot searchdomain.Snapshot, response *opensearch.Response) error {
	parsed := opensearch.ParseError(response)
	var structured *opensearch.StructError
	missing := response.StatusCode == http.StatusNotFound
	if errors.As(parsed, &structured) && structured.Err.Type == "search_context_missing_exception" {
		missing = true
	}
	if missing {
		telemetry.L(ctx).InfoContext(ctx, "search.snapshot.lost", slog.String("index", snapshot.Index), slog.String("reason", parsed.Error()))
		return queryStepError{operation: "read search snapshot on " + snapshot.Index, err: searchdomain.ErrSnapshotLost}
	}
	return rankFailure(ctx, snapshot.Index, fmt.Errorf("search request rejected: %w", engineCause(response, parsed)))
}

func rankFailure(ctx context.Context, index string, err error) error {
	wrapped := fmt.Errorf("read ranked search batch on %s: %w", index, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.query.read_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
	return wrapped
}
