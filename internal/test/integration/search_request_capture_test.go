package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
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
	// replacement is the point-in-time ID that the next ranked batch
	// response returns in place of the engine's pit_id.
	replacement string
	// deleted stores the point in time IDs from every DELETE /_search/point_in_time request
	// that the proxy forwarded.
	deleted []string
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
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	if capture.replacement != "" {
		payload, err = replacePITID(payload, capture.replacement)
		if err != nil {
			return err
		}
		capture.replacement = ""
	}
	response.Body = io.NopCloser(bytes.NewReader(payload))
	response.ContentLength = int64(len(payload))
	response.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	capture.searches = append(capture.searches, capturedSearch{Request: body, Response: payload})
	return nil
}

// replaceNextPIT makes the next recorded ranked batch response return
// replacement as its pit_id.
func (capture *searchCapture) replaceNextPIT(replacement string) {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.replacement = replacement
}

// replacePITID replaces the pit_id value of one ranked batch response body
// and keeps every other byte.
func replacePITID(payload []byte, replacement string) ([]byte, error) {
	var decoded pitResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode ranked search response: %w", err)
	}
	if decoded.PITID == "" {
		return nil, errors.New("ranked search response does not contain a pit_id")
	}
	original := []byte(`"pit_id":"` + decoded.PITID + `"`)
	if !bytes.Contains(payload, original) {
		return nil, errors.New("ranked search response does not contain the compact pit_id field")
	}
	return bytes.Replace(payload, original, []byte(`"pit_id":"`+replacement+`"`), 1), nil
}

// ServeHTTP forwards requests and prepares POST /_search recording. It adds profile: true when
// profiling is enabled and removes Accept-Encoding from ranked batch requests to obtain plain JSON.
// It records point in time delete IDs before forwarding each delete request.
func (capture *searchCapture) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodDelete && request.URL.Path == pitDeletePath {
		capture.recordDelete(writer, request)
		return
	}
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
	capture := &searchCapture{engine: nil, profile: profile, mutex: sync.Mutex{}, searches: nil, replacement: "", deleted: nil}
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
