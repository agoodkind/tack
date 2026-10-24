package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// predictPathPrefix selects model predict requests by their URL path prefix.
const predictPathPrefix = "/_plugins/_ml/_predict/"

// capturedPredictKey associates the original model predict body with its
// request context.
type capturedPredictKey struct{}

// inferenceExchange groups captured ranked batch and model predict exchanges.
type inferenceExchange struct {
	Searches []capturedSearch
	Predicts []capturedSearch
}

// inferenceCapture wraps searchCapture to record model predict request and
// response bodies in addition to ranked batches.
type inferenceCapture struct {
	search   *searchCapture
	mutex    sync.Mutex
	predicts []capturedSearch
}

// ServeHTTP prepares POST model predict exchanges for recording and delegates
// other requests to searchCapture. It removes Accept-Encoding from predict
// requests to obtain plain JSON.
func (capture *inferenceCapture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !strings.HasPrefix(request.URL.Path, predictPathPrefix) {
		capture.search.ServeHTTP(writer, request)
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "read predict body: "+err.Error(), http.StatusBadRequest)
		return
	}
	request = request.WithContext(context.WithValue(request.Context(), capturedPredictKey{}, body))
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Del("Accept-Encoding")
	capture.search.engine.ServeHTTP(writer, request)
}

// record saves the original predict request body and engine response body when
// the request context contains capturedPredictKey. It restores the response
// body for the client and delegates other responses to searchCapture.
func (capture *inferenceCapture) record(response *http.Response) error {
	body, ok := response.Request.Context().Value(capturedPredictKey{}).([]byte)
	if !ok {
		return capture.search.record(response)
	}
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return errors.New("read predict response: " + err.Error())
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.predicts = append(capture.predicts, capturedSearch{Request: body, Response: payload})
	return nil
}

// take returns the recorded ranked batch and model predict exchanges and
// clears each stored slice under its recorder's mutex.
func (capture *inferenceCapture) take() inferenceExchange {
	searches := capture.search.take()
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	predicts := capture.predicts
	capture.predicts = nil
	return inferenceExchange{Searches: searches, Predicts: predicts}
}

// newInferenceCaptureHarness starts a TLS proxy on IPv6 loopback and configures
// a second production graph to use its endpoint and certificate. It returns
// an MCP harness with the fixture's bearer token and the inference recorder.
func newInferenceCaptureHarness(t *testing.T, fixture integration.QueryFixture) (*integration.MCPHarness, *inferenceCapture) {
	t.Helper()
	engine := testenv.OpenSearchWithMemory(t, integration.NativeSearchMemoryBytes)
	target, err := url.Parse(engine.Endpoint)
	if err != nil {
		t.Fatalf("parse engine endpoint: %v", err)
	}
	engineCA := x509.NewCertPool()
	if !engineCA.AppendCertsFromPEM([]byte(engine.CA)) {
		t.Fatal("engine CA is invalid")
	}
	search := &searchCapture{engine: nil, profile: false, mutex: sync.Mutex{}, searches: nil, replacement: ""}
	capture := &inferenceCapture{search: search, mutex: sync.Mutex{}, predicts: nil}
	search.engine = &httputil.ReverseProxy{
		Rewrite:        func(outbound *httputil.ProxyRequest) { outbound.SetURL(target) },
		Transport:      &http.Transport{TLSClientConfig: &tls.Config{RootCAs: engineCA, MinVersion: tls.VersionTLS12}},
		ModifyResponse: capture.record,
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen for the inference capture proxy: %v", err)
	}
	server := httptest.NewUnstartedServer(capture)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "inference-capture-ca.pem")
	proxyCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, proxyCA, 0o600); err != nil {
		t.Fatalf("write inference capture proxy CA: %v", err)
	}
	cfg := integration.QueryConfig(t)
	cfg.SearchEndpoint = server.URL
	cfg.SearchCA = caPath
	return integration.ProcessHarness(t, fixture.Harness, cfg), capture
}

// oversizedQueryBytes exceeds the default 1,024-byte query limit by one byte.
const oversizedQueryBytes = 1025

// requireRejectedBeforeInference calls tack_search with an empty, a blank,
// and an oversized query. Each call must return an error result that
// contains the word query, and the proxy must record no model predict
// request and no ranked batch.
func requireRejectedBeforeInference(t *testing.T, harness *integration.MCPHarness, capture *inferenceCapture) {
	t.Helper()
	for _, query := range []string{"", "   ", strings.Repeat("q", oversizedQueryBytes)} {
		if _, err := integration.TrySearch(harness, query, ""); err == nil || !strings.Contains(err.Error(), "query") {
			t.Fatalf("search with a query of %d bytes returned error %v, want an error that contains the word query", len(query), err)
		}
	}
	if exchange := capture.take(); len(exchange.Predicts) != 0 || len(exchange.Searches) != 0 {
		t.Fatalf("rejected queries sent %d model predict requests and %d ranked batches, want none", len(exchange.Predicts), len(exchange.Searches))
	}
}

// predictResult decodes the inference results from a model predict response.
type predictResult struct {
	InferenceResults []predictInference `json:"inference_results"`
}

type predictInference struct {
	Output []predictOutput `json:"output"`
}

type predictOutput struct {
	DataAsMap predictData `json:"dataAsMap"`
}

type predictData struct {
	Response []map[string]float64 `json:"response"`
}

// requireOnePrediction requires exactly one model predict exchange and returns
// its token-weight map. It fails the test unless the response contains exactly
// one inference result, one output, and one map.
func requireOnePrediction(t *testing.T, exchange inferenceExchange) map[string]float64 {
	t.Helper()
	if len(exchange.Predicts) != 1 {
		t.Fatalf("the session sent %d model predict requests, want 1", len(exchange.Predicts))
	}
	var result predictResult
	if err := json.Unmarshal(exchange.Predicts[0].Response, &result); err != nil {
		t.Fatalf("decode model predict response: %v", err)
	}
	if len(result.InferenceResults) != 1 || len(result.InferenceResults[0].Output) != 1 ||
		len(result.InferenceResults[0].Output[0].DataAsMap.Response) != 1 {
		t.Fatalf("model predict response does not contain exactly one token-weight map: %s", exchange.Predicts[0].Response)
	}
	return result.InferenceResults[0].Output[0].DataAsMap.Response[0]
}
