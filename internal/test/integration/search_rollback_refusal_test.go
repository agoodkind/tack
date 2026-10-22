package integration_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/service"
)

// refusingWorker contains a production worker that uses a local TLS proxy
// and a switch that makes the proxy refuse requests to read page access.
type refusingWorker struct {
	worker *service.SearchWorker
	refuse *atomic.Bool
}

// newRefusingWorker builds a proxy to the real engine and a worker that
// uses it. While refuse is set, the proxy answers every request to read
// multiple documents with HTTP 503.
func newRefusingWorker(t *testing.T, fixture rolloutFixture) refusingWorker {
	t.Helper()
	target, err := url.Parse(fixture.engine.Endpoint)
	if err != nil {
		t.Fatalf("cannot parse engine endpoint: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(fixture.engine.CA))
	forward := httputil.NewSingleHostReverseProxy(target)
	forward.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	refuse := &atomic.Bool{}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if refuse.Load() && strings.HasSuffix(request.URL.Path, "/_mget") {
			http.Error(writer, "the test refuses this request to read multiple documents", http.StatusServiceUnavailable)
			return
		}
		request.Host = target.Host
		forward.ServeHTTP(writer, request)
	})
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("cannot listen for proxy requests: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	authority := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	configuration := rolloutSearchConfig(fixture.engine)
	configuration.Endpoint, configuration.CA = server.URL, string(authority)
	adapter, err := search.New(t.Context(), configuration)
	if err != nil {
		t.Fatalf("cannot create adapter behind proxy: %v", err)
	}
	t.Cleanup(func() { _ = adapter.Close(context.WithoutCancel(t.Context())) })
	return refusingWorker{worker: newRolloutWorker(t, fixture.stores, adapter), refuse: refuse}
}
