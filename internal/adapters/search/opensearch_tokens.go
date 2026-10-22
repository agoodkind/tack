package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// modelPredictRequest asks ML Commons for the sparse token weights of one
// query text. [opensearch.Do] sends it because the opensearch-go v4.7.3
// client has no ML Commons API.
type modelPredictRequest struct {
	modelID string
	body    []byte
}

func (request modelPredictRequest) GetRequest(method string) (*http.Request, error) {
	if request.modelID == "" {
		return nil, errors.New("ML model ID is empty")
	}
	path := "/_plugins/_ml/_predict/sparse_encoding/" + url.PathEscape(request.modelID)
	return jsonBodyRequest(method, path, request.body, "build model predict request")
}

type predictBody struct {
	TextDocs []string `json:"text_docs"`
}

type predictResponse struct {
	InferenceResults []struct {
		Output []struct {
			DataAsMap struct {
				Response []json.RawMessage `json:"response"`
			} `json:"dataAsMap"`
		} `json:"output"`
	} `json:"inference_results"`
}

// predictQueryTokens runs the pinned model once and returns the exact
// token-weight JSON. It requires exactly one nonempty map of finite,
// nonnegative weights within maxBytes. When the memory circuit breaker still
// rejects the predict after the client retries, the returned error wraps
// [searchdomain.ErrEngineUnavailable].
func (a *Adapter) predictQueryTokens(ctx context.Context, modelID, text string, maxBytes int) (json.RawMessage, error) {
	body, err := json.Marshal(predictBody{TextDocs: []string{text}})
	if err != nil {
		return nil, tokenFailure(ctx, modelID, fmt.Errorf("encode prediction request: %w", err))
	}
	var decoded predictResponse
	response, err := opensearch.Do(ctx, a.client, http.MethodPost, modelPredictRequest{modelID: modelID, body: body}, &decoded)
	if err != nil {
		return nil, tokenFailure(ctx, modelID, fmt.Errorf("predict query tokens: %w", engineCause(response, err)))
	}
	if response == nil {
		return nil, tokenFailure(ctx, modelID, errors.New("predict query tokens: OpenSearch returned no response"))
	}
	if response.IsError() {
		parsed := opensearch.ParseError(response)
		if breakerRejected(response.StatusCode, parsed) {
			parsed = errors.Join(searchdomain.ErrEngineUnavailable, parsed)
		}
		return nil, tokenFailure(ctx, modelID, fmt.Errorf("predict query tokens: %w", engineCause(response, parsed)))
	}
	if len(decoded.InferenceResults) != 1 || len(decoded.InferenceResults[0].Output) != 1 ||
		len(decoded.InferenceResults[0].Output[0].DataAsMap.Response) != 1 {
		return nil, tokenFailure(ctx, modelID, errors.New("prediction must return exactly one token-weight map"))
	}
	raw := decoded.InferenceResults[0].Output[0].DataAsMap.Response[0]
	if err := validateTokenWeights(raw, maxBytes); err != nil {
		return nil, tokenFailure(ctx, modelID, err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, tokenFailure(ctx, modelID, fmt.Errorf("compact query tokens: %w", err))
	}
	return compact.Bytes(), nil
}

func validateTokenWeights(raw json.RawMessage, maxBytes int) error {
	if len(raw) > maxBytes {
		operation := fmt.Sprintf("query tokens contain %d bytes, above the %d-byte session bound", len(raw), maxBytes)
		return queryStepError{operation: operation, err: searchdomain.ErrInvalidQuery}
	}
	var weights map[string]float64
	if err := json.Unmarshal(raw, &weights); err != nil {
		return queryStepError{operation: "decode query tokens", err: err}
	}
	if len(weights) == 0 {
		return queryStepError{operation: "query tokens are empty", err: searchdomain.ErrInvalidQuery}
	}
	for token, weight := range weights {
		if token == "" || math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
			return errors.New("query token " + strconv.Quote(token) + " has invalid weight " + strconv.FormatFloat(weight, 'g', -1, 64))
		}
	}
	return nil
}

func tokenFailure(ctx context.Context, modelID string, err error) error {
	wrapped := fmt.Errorf("compute query tokens with model %s: %w", modelID, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.query.tokens_failed", slog.String("err", wrapped.Error()), slog.String("model_id", modelID))
	return wrapped
}

// breakerExceptionType is the OpenSearch error type of a circuit breaker
// rejection.
const breakerExceptionType = "circuit_breaking_exception"

// breakerRejected reports whether a predict failed with HTTP status 429 and
// the circuit breaker exception type.
func breakerRejected(status int, parsed error) bool {
	var structured *opensearch.StructError
	return status == http.StatusTooManyRequests && errors.As(parsed, &structured) && structured.Err.Type == breakerExceptionType
}
