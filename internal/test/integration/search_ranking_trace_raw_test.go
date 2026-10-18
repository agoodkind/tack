package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"testing"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

type traceMLRequest struct {
	path string
	body []byte
}

func (request traceMLRequest) GetRequest(method string) (*http.Request, error) {
	result, err := http.NewRequestWithContext(context.Background(), method, request.path, bytes.NewReader(request.body))
	if err != nil {
		return nil, fmt.Errorf("build trace request %s: %w", request.path, err)
	}
	result.Header.Set("Content-Type", "application/json")
	return result, nil
}

type tracePredictResponse struct {
	InferenceResults []struct {
		Output []struct {
			DataAsMap struct {
				Response []map[string]float64 `json:"response"`
			} `json:"dataAsMap"`
		} `json:"output"`
	} `json:"inference_results"`
}

// indexModel reads an OpenSearch index mapping and returns its _meta.model_id value.
// It fails the test when that field is empty.
func (r *rankingRun) indexModel(t *testing.T, index string) string {
	t.Helper()
	response, err := r.client.Indices.Mapping.Get(t.Context(), &opensearchapi.MappingGetReq{Indices: []string{index}})
	if err != nil {
		t.Fatalf("read mapping of %s: %v", index, err)
	}
	var mapping struct {
		Meta struct {
			ModelID string `json:"model_id"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(response.GetIndices()[index].Mappings, &mapping); err != nil {
		t.Fatalf("decode mapping of %s: %v", index, err)
	}
	if mapping.Meta.ModelID == "" {
		t.Fatalf("mapping of %s has no model ID", index)
	}
	return mapping.Meta.ModelID
}

// predictTraceWeights sends one ML Commons sparse_encoding predict request for text and returns
// the complete token-weight map without pruning. The semantic field applies max_ratio pruning
// to stored chunk embeddings.
func (r *rankingRun) predictTraceWeights(t *testing.T, modelID, text string) map[string]float64 {
	t.Helper()
	body, err := json.Marshal(map[string][]string{"text_docs": {text}})
	if err != nil {
		t.Fatalf("encode trace prediction: %v", err)
	}
	request := traceMLRequest{path: "/_plugins/_ml/_predict/sparse_encoding/" + url.PathEscape(modelID), body: body}
	var decoded tracePredictResponse
	response, err := opensearch.Do(t.Context(), r.client.Client, http.MethodPost, request, &decoded)
	if err != nil {
		t.Fatalf("predict trace weights: %v", err)
	}
	if response.IsError() {
		t.Fatalf("predict trace weights: %v", opensearch.ParseError(response))
	}
	if len(decoded.InferenceResults) != 1 || len(decoded.InferenceResults[0].Output) != 1 ||
		len(decoded.InferenceResults[0].Output[0].DataAsMap.Response) != 1 {
		t.Fatal("trace prediction must return exactly one token-weight map")
	}
	return decoded.InferenceResults[0].Output[0].DataAsMap.Response[0]
}

// traceRawChunk calls predictTraceWeights for one generated chunk and logs the saved query tokens'
// dot products with the unpruned weights and the stored chunk embedding. It also logs the largest
// unpruned weight, both maps' token counts, and each query token with a positive unpruned weight
// that does not appear in the stored embedding, including its unpruned weight and query weight.
func (r *rankingRun) traceRawChunk(t *testing.T, modelID, query, name string, page, number int, text string, stored, weights map[string]float64) {
	t.Helper()
	raw := r.predictTraceWeights(t, modelID, text)
	rawMax := 0.0
	for _, weight := range raw {
		rawMax = max(rawMax, weight)
	}
	omitted := make([]string, 0, len(weights))
	for token := range weights {
		if _, kept := stored[token]; !kept && raw[token] > 0 {
			omitted = append(omitted, token)
		}
	}
	sort.Slice(omitted, func(i, j int) bool {
		return weights[omitted[i]]*raw[omitted[i]] > weights[omitted[j]]*raw[omitted[j]]
	})
	parts := make([]string, 0, len(omitted))
	for _, token := range omitted {
		parts = append(parts, fmt.Sprintf("%q:raw=%.4f,query=%.3f", token, raw[token], weights[token]))
	}
	t.Logf("trace query=%q raw chunk name=%q page=%d chunk=%d raw_dot=%.4f stored_dot=%.4f raw_max=%.4f raw_tokens=%d stored_tokens=%d omitted=[%s]",
		query, name, page, number, traceDot(weights, raw), traceDot(weights, stored), rawMax, len(raw), len(stored), strings.Join(parts, " "))
}
