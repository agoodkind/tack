package integration

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
	"time"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/testenv"
)

const (
	// taskPollRequestTimeout is the production default of
	// OPENSEARCH_REQUEST_TIMEOUT.
	taskPollRequestTimeout = 10 * time.Second
	// taskPollMaxRetries is the production default of OPENSEARCH_MAX_RETRIES.
	taskPollMaxRetries = 2
	// taskPollPath is the path prefix of an ML Commons task-status request.
	taskPollPath = "/_plugins/_ml/tasks/"
)

// taskPollStaller forwards every request to the engine. It delays the first
// stalledRequests task-status requests until the client abandons them.
type taskPollStaller struct {
	engine          *httputil.ReverseProxy
	stalledRequests int32
	polls           atomic.Int32
}

func (staller *taskPollStaller) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	taskPoll := request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, taskPollPath)
	if taskPoll && staller.polls.Add(1) <= staller.stalledRequests {
		<-request.Context().Done()
		return
	}
	staller.engine.ServeHTTP(writer, request)
}

// newTaskPollStaller starts a TLS proxy in front of the engine. Every attempt
// of the first task-status poll exceeds the request timeout.
func newTaskPollStaller(t *testing.T, fixture testenv.OpenSearchFixture) (*taskPollStaller, search.Config) {
	t.Helper()
	target, err := url.Parse(fixture.Endpoint)
	if err != nil {
		t.Fatalf("parse engine endpoint: %v", err)
	}
	engineCA := x509.NewCertPool()
	if !engineCA.AppendCertsFromPEM([]byte(fixture.CA)) {
		t.Fatal("engine CA is invalid")
	}
	engine := &httputil.ReverseProxy{
		Rewrite: func(outbound *httputil.ProxyRequest) { outbound.SetURL(target) },
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: engineCA, MinVersion: tls.VersionTLS12},
		},
	}
	staller := &taskPollStaller{engine: engine, stalledRequests: taskPollMaxRetries + 1, polls: atomic.Int32{}}
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen for the engine proxy: %v", err)
	}
	server := httptest.NewUnstartedServer(staller)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	proxyCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	pass := fixture.Password
	configuration := search.Config{
		Endpoint: server.URL, CA: string(proxyCA), Username: fixture.Username,
		Password: pass, RequestTimeout: taskPollRequestTimeout, MaxRetries: taskPollMaxRetries,
	}
	return staller, configuration
}

// TestSearchModelTaskPollTimeout undeploys the pinned model and provisions it
// through a proxy that delays every attempt of the first deploy task-status
// poll past the request timeout. Provision must succeed and leave the model
// DEPLOYED.
func TestSearchModelTaskPollTimeout(t *testing.T) {
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	undeployNativeModel(t, adapter, client, model.ID)
	staller, configuration := newTaskPollStaller(t, testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes))
	proxied, err := search.New(t.Context(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := proxied.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	if _, err := proxied.Provision(t.Context()); err != nil {
		t.Fatalf("provision after one task-status poll timed out: %v", err)
	}
	if polls := staller.polls.Load(); polls <= staller.stalledRequests {
		t.Fatalf("proxy received %d task-status requests, want more than the %d it delayed", polls, staller.stalledRequests)
	}
	if state := nativeModelState(t, client, model.ID); state != "DEPLOYED" {
		t.Fatalf("model %s state is %s, want DEPLOYED", model.ID, state)
	}
}
