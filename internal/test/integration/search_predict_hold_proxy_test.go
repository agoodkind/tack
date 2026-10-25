package integration_test

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
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

// predictHoldProxy is a TLS proxy in front of the shared engine. It forwards
// every request. After arm, it counts each model predict request and keeps
// the first one open until release.
type predictHoldProxy struct {
	engine   *httputil.ReverseProxy
	held     chan struct{}
	released chan struct{}
	mutex    sync.Mutex
	armed    bool
	holding  bool
	predicts int
}

// ServeHTTP keeps the first model predict request after arm open until
// release or until the client abandons it, then forwards every request.
func (proxy *predictHoldProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasPrefix(request.URL.Path, predictPathPrefix) && proxy.hold() {
		select {
		case <-proxy.released:
		case <-request.Context().Done():
			return
		}
	}
	proxy.engine.ServeHTTP(writer, request)
}

// hold counts one model predict request and reports whether the proxy keeps
// it open. Only the first predict request after arm stays open.
func (proxy *predictHoldProxy) hold() bool {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	if !proxy.armed {
		return false
	}
	proxy.predicts++
	if proxy.holding {
		return false
	}
	proxy.holding = true
	close(proxy.held)
	return true
}

// arm starts counting model predict requests and keeping the first one open.
func (proxy *predictHoldProxy) arm() {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.armed = true
}

// release forwards the model predict request that the proxy keeps open. A
// second call does nothing.
func (proxy *predictHoldProxy) release() {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	select {
	case <-proxy.released:
	default:
		close(proxy.released)
	}
}

// predictCount returns the model predict requests the proxy received after
// arm.
func (proxy *predictHoldProxy) predictCount() int {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return proxy.predicts
}

// newPredictHoldHarness starts a predictHoldProxy on IPv6 loopback and builds
// a second production graph that sends every engine request through it. The
// graph reads the search query settings from the environment at build time.
// It returns an MCP harness with the fixture's bearer token and the proxy.
func newPredictHoldHarness(t *testing.T, fixture integration.QueryFixture) (*integration.MCPHarness, *predictHoldProxy) {
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
	proxy := &predictHoldProxy{
		engine: &httputil.ReverseProxy{
			Rewrite:   func(outbound *httputil.ProxyRequest) { outbound.SetURL(target) },
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: engineCA, MinVersion: tls.VersionTLS12}},
		},
		held: make(chan struct{}), released: make(chan struct{}),
		mutex: sync.Mutex{}, armed: false, holding: false, predicts: 0,
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen for the predict hold proxy: %v", err)
	}
	server := httptest.NewUnstartedServer(proxy)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	t.Cleanup(proxy.release)
	caPath := filepath.Join(t.TempDir(), "predict-hold-ca.pem")
	proxyCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, proxyCA, 0o600); err != nil {
		t.Fatalf("write predict hold proxy CA: %v", err)
	}
	cfg := integration.QueryConfig(t)
	cfg.SearchEndpoint = server.URL
	cfg.SearchCA = caPath
	return integration.ProcessHarness(t, fixture.Harness, cfg), proxy
}
