package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
	"time"

	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// predictPathPrefix selects model predict requests by their URL path prefix.
const predictPathPrefix = "/_plugins/_ml/_predict/"

// capturedPredictKey associates the original model predict body with its
// request context.
type capturedPredictKey struct{}

// inferenceExchange groups captured ranked batch and model predict exchanges
// with the count of model predict requests that arrived at the proxy.
// The count includes requests that the engine never answered.
type inferenceExchange struct {
	Searches []capturedSearch
	Predicts []capturedSearch
	Arrivals int
}

// inferenceCapture wraps searchCapture to record model predict request and
// response bodies and ranked batches. It counts arriving model predict
// requests and can delay the next one.
type inferenceCapture struct {
	search   *searchCapture
	mutex    sync.Mutex
	predicts []capturedSearch
	// arrivals counts model predict requests that arrived at the proxy since
	// the last take.
	arrivals int
	// hold delays the next arriving model predict request before the proxy
	// forwards it. A zero value makes the proxy forward it immediately.
	hold time.Duration
}

// ServeHTTP counts each POST model predict request, applies a pending hold,
// and prepares the exchange for recording. It sends other requests to
// searchCapture. It drops a delayed request when the client cancels it during
// the hold. It removes Accept-Encoding from predict requests to obtain plain
// JSON.
func (capture *inferenceCapture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || !strings.HasPrefix(request.URL.Path, predictPathPrefix) {
		capture.search.ServeHTTP(writer, request)
		return
	}
	if !capture.admitPredict(request.Context()) {
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

// take returns the recorded ranked batch and model predict exchanges and the
// predict arrival count. It clears each stored slice and the count under the
// corresponding recorder's mutex.
func (capture *inferenceCapture) take() inferenceExchange {
	searches := capture.search.take()
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	predicts, arrivals := capture.predicts, capture.arrivals
	capture.predicts, capture.arrivals = nil, 0
	return inferenceExchange{Searches: searches, Predicts: predicts, Arrivals: arrivals}
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
	capture := &inferenceCapture{search: search, mutex: sync.Mutex{}, predicts: nil, arrivals: 0, hold: 0}
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
	if exchange := capture.take(); exchange.Arrivals != 0 || len(exchange.Searches) != 0 {
		t.Fatalf("rejected queries sent %d model predict requests and %d ranked batches, want none", exchange.Arrivals, len(exchange.Searches))
	}
}
