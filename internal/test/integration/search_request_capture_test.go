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
	"sync"
	"testing"

	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// rankedSearchPath selects ranked batch requests that search through a
// point in time without an index path.
const rankedSearchPath = "/_search"

// capturedSearch pairs an original ranked batch request body with the
// corresponding engine response body.
type capturedSearch struct {
	Request  []byte
	Response []byte
}

// searchCapture forwards requests to OpenSearch and records ranked batch
// exchanges under a mutex.
type searchCapture struct {
	engine   *httputil.ReverseProxy
	profile  bool
	mutex    sync.Mutex
	searches []capturedSearch
}

// capturedBodyKey associates the original ranked batch body with its
// request context.
type capturedBodyKey struct{}

// record saves the original request body and engine response body when the
// request context contains capturedBodyKey. It restores the response body
// for the client.
func (capture *searchCapture) record(response *http.Response) error {
	body, ok := response.Request.Context().Value(capturedBodyKey{}).([]byte)
	if !ok {
		return nil
	}
	payload, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return errors.New("read ranked search response: " + err.Error())
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.searches = append(capture.searches, capturedSearch{Request: body, Response: payload})
	return nil
}

// ServeHTTP forwards requests and prepares POST /_search exchanges for
// recording. It adds profile: true when profiling is enabled and removes
// Accept-Encoding from ranked batch requests to obtain plain JSON.
func (capture *searchCapture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != rankedSearchPath {
		capture.engine.ServeHTTP(writer, request)
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "read ranked search body: "+err.Error(), http.StatusBadRequest)
		return
	}
	forwarded := body
	if capture.profile {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			http.Error(writer, "decode ranked search body: "+err.Error(), http.StatusBadRequest)
			return
		}
		fields["profile"] = json.RawMessage("true")
		if forwarded, err = json.Marshal(fields); err != nil {
			http.Error(writer, "encode profiled search body: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	request = request.WithContext(context.WithValue(request.Context(), capturedBodyKey{}, body))
	request.Body = io.NopCloser(bytes.NewReader(forwarded))
	request.ContentLength = int64(len(forwarded))
	request.Header.Del("Accept-Encoding")
	capture.engine.ServeHTTP(writer, request)
}

// take returns the recorded ranked batches and clears the stored slice
// under the mutex.
func (capture *searchCapture) take() []capturedSearch {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	searches := capture.searches
	capture.searches = nil
	return searches
}

// newCaptureHarness starts a TLS proxy on IPv6 loopback and configures a
// second production graph to use its endpoint and certificate. It returns
// an MCP harness with the fixture's bearer token and the proxy recorder.
func newCaptureHarness(t *testing.T, fixture integration.QueryFixture, profile bool) (*integration.MCPHarness, *searchCapture) {
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
	capture := &searchCapture{engine: nil, profile: profile, mutex: sync.Mutex{}, searches: nil}
	capture.engine = &httputil.ReverseProxy{
		Rewrite:        func(outbound *httputil.ProxyRequest) { outbound.SetURL(target) },
		Transport:      &http.Transport{TLSClientConfig: &tls.Config{RootCAs: engineCA, MinVersion: tls.VersionTLS12}},
		ModifyResponse: capture.record,
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen for the search capture proxy: %v", err)
	}
	server := httptest.NewUnstartedServer(capture)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	caPath := filepath.Join(t.TempDir(), "capture-ca.pem")
	proxyCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, proxyCA, 0o600); err != nil {
		t.Fatalf("write capture proxy CA: %v", err)
	}
	cfg := integration.QueryConfig(t)
	cfg.SearchEndpoint = server.URL
	cfg.SearchCA = caPath
	return integration.ProcessHarness(t, fixture.Harness, cfg), capture
}
