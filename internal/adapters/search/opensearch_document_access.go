package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
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
		Versions   []string `json:"versions"`
		Generation int64    `json:"generation"`
	} `json:"access"`
}

// DocumentAccess reads the retirement flag, access versions, and access
// generation of exact page documents in index with one OpenSearch multi-get
// request. The multi-get read is realtime and does not require an index
// refresh. A missing document returns with Found set to false.
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
		Index: index, BodyReader: bytes.NewReader(body),
		Params: &opensearchapi.MGetParams{SourceIncludes: []string{"retired", "access.versions", "access.generation"}},
	})
	if err != nil {
		return nil, documentAccessFailure(ctx, index, "read page documents", err)
	}
	for _, result := range response.Docs {
		if result.Type() == opensearchapi.MGetRespItemMultiGetErrorType {
			failure, err := result.MultiGetError()
			if err != nil {
				return nil, documentAccessFailure(ctx, index, "decode page document error", err)
			}
			reason := failure.Error.Type
			if failure.Error.Reason != nil {
				reason += ": " + *failure.Error.Reason
			}
			return nil, documentAccessFailure(ctx, index, "read page document "+failure.ID, errors.New(reason))
		}
		document, err := result.GetResult()
		if err != nil {
			return nil, documentAccessFailure(ctx, index, "decode page document", err)
		}
		state := searchdomain.DocumentAccess{Found: document.Found, Retired: false, Versions: nil, Generation: 0}
		if document.Found {
			var source documentAccessSource
			if err := json.Unmarshal(document.Source, &source); err != nil {
				return nil, documentAccessFailure(ctx, index, "decode page document "+document.ID, err)
			}
			state.Retired, state.Versions, state.Generation = source.Retired, source.Access.Versions, source.Access.Generation
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
