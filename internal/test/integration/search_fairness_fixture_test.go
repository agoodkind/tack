package integration_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/service"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

// pacingProxy delays bulk requests to the real OpenSearch test engine.
// Requests with page_text in the first document wait pageDelay. Other bulk
// requests wait otherDelay. Other requests proceed without a delay. It
// returns HTTP 503 if the client abandons a request during the wait.
type pacingProxy struct {
	engine *httputil.ReverseProxy

	mutex      sync.Mutex
	pageDelay  time.Duration
	otherDelay time.Duration
}

// pacedBulkBody contains the bulk document field that identifies a content
// page write.
type pacedBulkBody struct {
	PageText *string `json:"page_text"`
}

func (proxy *pacingProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !strings.HasSuffix(request.URL.Path, "/_bulk") {
		proxy.engine.ServeHTTP(writer, request)
		return
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, "read bulk body: "+err.Error(), http.StatusBadRequest)
		return
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	var document pacedBulkBody
	lines := bytes.Split(body, []byte("\n"))
	pageWrite := len(lines) >= 2 && json.Unmarshal(lines[1], &document) == nil && document.PageText != nil
	proxy.mutex.Lock()
	delay := proxy.otherDelay
	if pageWrite {
		delay = proxy.pageDelay
	}
	proxy.mutex.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-request.Context().Done():
		http.Error(writer, "the client abandoned the paced bulk request", http.StatusServiceUnavailable)
		return
	case <-timer.C:
	}
	proxy.engine.ServeHTTP(writer, request)
}

// pace sets the page write delay and other bulk request delay for later
// requests.
func (proxy *pacingProxy) pace(pageDelay, otherDelay time.Duration) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.pageDelay, proxy.otherDelay = pageDelay, otherDelay
}

// newPacedMigrationEnv creates a migrationEnv with an Adapter and Worker
// that use a TLS pacingProxy for every engine request. Both proxy delays
// start at zero. Client reads the engine directly.
func newPacedMigrationEnv(t *testing.T, pageBytes int) (*migrationEnv, *pacingProxy) {
	t.Helper()
	env := newMigrationEnv(t, pageBytes)
	fixture := testenv.OpenSearchWithMemory(t, migrationEngineMemory)
	target, err := url.Parse(fixture.Endpoint)
	if err != nil {
		t.Fatalf("parse engine endpoint: %v", err)
	}
	engineCA := x509.NewCertPool()
	if !engineCA.AppendCertsFromPEM([]byte(fixture.CA)) {
		t.Fatal("engine CA is invalid")
	}
	proxy := &pacingProxy{
		engine: &httputil.ReverseProxy{
			Rewrite:   func(outbound *httputil.ProxyRequest) { outbound.SetURL(target) },
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: engineCA, MinVersion: tls.VersionTLS12}},
		},
		mutex: sync.Mutex{}, pageDelay: 0, otherDelay: 0,
	}
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen for the pacing proxy: %v", err)
	}
	server := httptest.NewUnstartedServer(proxy)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	proxyCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	pass := fixture.Password
	adapter, err := search.New(t.Context(), search.Config{
		Endpoint: server.URL, CA: string(proxyCA), Username: fixture.Username,
		Password: pass, RequestTimeout: 2 * time.Minute, MaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("create paced adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("close paced adapter: %v", err)
		}
	})
	env.Adapter = adapter
	env.Worker = env.newWorker(t, env.Stores)
	return env, proxy
}

// newFairnessWorker builds a worker over env with class weights from
// config.LoadSearchWorkerSettings, the given slice budget, and offset zero.
func newFairnessWorker(t *testing.T, env *migrationEnv, sliceBudget time.Duration) *service.SearchWorker {
	t.Helper()
	production, err := config.LoadSearchWorkerSettings(integration.WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatalf("load search worker settings: %v", err)
	}
	settings := config.SearchWorkerSettings{
		PageBytes: env.PageBytes, MaxPages: 32, MaxBytes: 5 << 20, SliceBudget: sliceBudget,
		OperationTimeout: 10 * time.Second, Lease: 30 * time.Second, IdleInterval: 50 * time.Millisecond,
		Concurrency: 1, ClassWeights: production.ClassWeights,
		ReplacementPauseLimit: config.SearchReplacementPauseLimit, WriteCompletionHorizon: env.WriteHorizon,
	}
	policies := env.Stores.SearchPolicySet()
	rollouts := env.Stores.SearchRollouts(clock.Wall{}, policies)
	worker, err := service.NewSearchWorker(service.SearchWorkerPorts{
		Store: env.Stores.SearchWork(clock.Wall{}), Reader: env.Stores.SearchContent(policies),
		Access: env.Stores.SearchAccess(policies), Writer: env.Adapter, Rollouts: rollouts, Sessions: rollouts,
		Documents: env.Adapter, Rebuilds: env.Stores.SearchRebuilds(clock.Wall{}), Replacer: env.Adapter,
		Audit: appruntime.SearchWorkerAudit(env.Stores),
	}, clock.Wall{}, settings, 0)
	if err != nil {
		t.Fatalf("create the fairness worker: %v", err)
	}
	return worker
}
