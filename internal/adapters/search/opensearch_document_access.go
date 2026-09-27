package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

var _ searchdomain.DocumentAccessReader = (*Adapter)(nil)

// multiGetBody lists the IDs of the documents one multi-get reads.
type multiGetBody struct {
	IDs []string `json:"ids"`
}

// documentAccessSource is the part of a page document the access rollout
// verification reads.
type documentAccessSource struct {
	Retired bool `json:"retired"`
	Access  struct {
		Versions []string `json:"versions"`
	} `json:"access"`
}

// DocumentAccess reads the retirement flag and access versions of exact page
// documents in index with one OpenSearch multi-get request. The multi-get
// read is realtime and does not require an index refresh. A missing document
// returns with Found set to false.
func (a *Adapter) DocumentAccess(ctx context.Context, index string, ids []string) (map[string]searchdomain.DocumentAccess, error) {
	states := make(map[string]searchdomain.DocumentAccess, len(ids))
	if len(ids) == 0 {
		return states, nil
	}
	body, err := json.Marshal(multiGetBody{IDs: ids})
	if err != nil {
		return nil, documentAccessFailure(ctx, index, "encode multi-get request", err)
	}
	response, err := a.api.MGet(ctx, opensearchapi.MGetReq{
		Index: index, Body: bytes.NewReader(body),
		Params: opensearchapi.MGetParams{SourceIncludes: []string{"retired", "access.versions"}},
	})
	if err != nil {
		return nil, documentAccessFailure(ctx, index, "read page documents", err)
	}
	for _, document := range response.Docs {
		if document.Error != nil {
			return nil, documentAccessFailure(ctx, index, "read page document "+document.ID,
				fmt.Errorf("%s: %s", document.Error.Type, document.Error.Reason))
		}
		state := searchdomain.DocumentAccess{Found: document.Found, Retired: false, Versions: nil}
		if document.Found {
			var source documentAccessSource
			if err := json.Unmarshal(document.Source, &source); err != nil {
				return nil, documentAccessFailure(ctx, index, "decode page document "+document.ID, err)
			}
			state.Retired, state.Versions = source.Retired, source.Access.Versions
		}
		states[document.ID] = state
	}
	return states, nil
}

func documentAccessFailure(ctx context.Context, index, operation string, err error) error {
	wrapped := fmt.Errorf("%s in %s: %w", operation, index, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.documents.read_failed", slog.String("err", wrapped.Error()), slog.String("index", index))
	return wrapped
}
