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
	"slices"
	"sync"
	"testing"

	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
)

type searchReadRequest struct {
	method string
	path   string
}

// searchReadProxy forwards unchanged exchanges and records only method/path metadata.
type searchReadProxy struct {
	engine   *httputil.ReverseProxy
	mutex    sync.Mutex
	requests []searchReadRequest
}

func (proxy *searchReadProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	proxy.mutex.Lock()
	proxy.requests = append(proxy.requests, searchReadRequest{method: request.Method, path: request.URL.Path})
	proxy.mutex.Unlock()
	proxy.engine.ServeHTTP(writer, request)
}

func (proxy *searchReadProxy) observed() []searchReadRequest {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return slices.Clone(proxy.requests)
}

func newSearchReadProxy(t *testing.T, cfg *config.Config) (*config.Config, *searchReadProxy) {
	t.Helper()
	fixture := testenv.OpenSearch(t)
	target, err := url.Parse(fixture.Endpoint)
	if err != nil {
		t.Fatalf("parse the real search endpoint: %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(fixture.CA)) {
		t.Fatal("the real search fixture CA is invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	proxy := &searchReadProxy{
		engine: &httputil.ReverseProxy{
			Rewrite:   func(request *httputil.ProxyRequest) { request.SetURL(target) },
			Transport: transport,
		},
		mutex: sync.Mutex{}, requests: nil,
	}
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen for real search forwarding: %v", err)
	}
	server := httptest.NewUnstartedServer(proxy)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	authority := filepath.Join(t.TempDir(), "operator-read-proxy-ca.pem")
	if err := os.WriteFile(authority, certificate, 0o600); err != nil {
		t.Fatalf("write the test forwarding CA: %v", err)
	}
	forwarded := *cfg
	forwarded.SearchEndpoint, forwarded.SearchCA = server.URL, authority
	return &forwarded, proxy
}
