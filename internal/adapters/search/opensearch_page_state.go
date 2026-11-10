package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/opensearch-project/opensearch-go/v5/opensearchapi"
)

// pageState is the stored generation, retirement flag, and sequence number
// of one page document. Found is false for an absent document.
type pageState struct {
	Found       bool
	Retired     bool
	Generation  int64
	SeqNo       int64
	PrimaryTerm int64
}

// pageStateSource is the part of a stored page document a guarded write
// compares.
type pageStateSource struct {
	Retired          bool  `json:"retired"`
	SearchGeneration int64 `json:"search_generation"`
}

// readPageStates reads the stored generation, retirement flag, sequence
// number, and primary term of each document in index with one realtime
// multi-get request.
func (a *Adapter) readPageStates(ctx context.Context, index string, ids []string) (map[string]pageState, error) {
	states := make(map[string]pageState, len(ids))
	body, err := json.Marshal(multiGetBody{IDs: ids})
	if err != nil {
		return nil, documentAccessFailure(ctx, index, "encode page state request", err)
	}
	response, err := a.api.MGet(ctx, opensearchapi.MGetReq{
		Index: index, BodyReader: bytes.NewReader(body),
		Params: &opensearchapi.MGetParams{SourceIncludes: []string{"retired", "search_generation"}},
	})
	if err != nil {
		return nil, documentAccessFailure(ctx, index, "read page states", err)
	}
	for _, result := range response.Docs {
		if result.Type() == opensearchapi.MGetRespItemMultiGetErrorType {
			failure, err := result.MultiGetError()
			if err != nil {
				return nil, documentAccessFailure(ctx, index, "decode page state error", err)
			}
			reason := failure.Error.Type
			if failure.Error.Reason != nil {
				reason += ": " + *failure.Error.Reason
			}
			return nil, documentAccessFailure(ctx, index, "read page state "+failure.ID, errors.New(reason))
		}
		document, err := result.GetResult()
		if err != nil {
			return nil, documentAccessFailure(ctx, index, "decode page state", err)
		}
		state := pageState{Found: document.Found, Retired: false, Generation: 0, SeqNo: 0, PrimaryTerm: 0}
		if document.Found {
			var source pageStateSource
			if err := json.Unmarshal(document.Source, &source); err != nil {
				return nil, documentAccessFailure(ctx, index, "decode page state "+document.ID, err)
			}
			state.Retired, state.Generation = source.Retired, source.SearchGeneration
			if document.SeqNo == nil || document.PrimaryTerm == nil {
				return nil, documentAccessFailure(ctx, index, "read page state "+document.ID, errors.New("sequence number and primary term are required"))
			}
			state.SeqNo, state.PrimaryTerm = *document.SeqNo, *document.PrimaryTerm
		}
		states[document.ID] = state
	}
	return states, nil
}
