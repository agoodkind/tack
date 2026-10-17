package integration

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

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/testenv"
)

// delayedPageWrite is one page write the delay proxy received.
type delayedPageWrite struct {
	nodeID   string
	ordinal  uint64
	received time.Time
}

// pageDelayProxy forwards every request to the engine. It delays the first
// bulk page write of one node and ordinal. A positive delay forwards that
// write after the delay; a zero delay waits until the client abandons it.
type pageDelayProxy struct {
	engine  *httputil.ReverseProxy
	nodeID  string
	ordinal uint64
	delay   time.Duration
	started chan struct{}

	mutex     sync.Mutex
	delayed   bool
	heldFrom  time.Time
	heldUntil time.Time
	abandoned bool
	writes    []delayedPageWrite
}

// delayedPageBody is the part of a bulk page document the proxy reads.
type delayedPageBody struct {
	NodeID      string  `json:"node_id"`
	PageOrdinal uint64  `json:"page_ordinal"`
	PageText    *string `json:"page_text"`
}

func (proxy *pageDelayProxy) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
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
	var page delayedPageBody
	lines := bytes.Split(body, []byte("\n"))
	if len(lines) < 2 || json.Unmarshal(lines[1], &page) != nil || page.PageText == nil {
		proxy.engine.ServeHTTP(writer, request)
		return
	}
	now := time.Now()
	proxy.mutex.Lock()
	proxy.writes = append(proxy.writes, delayedPageWrite{nodeID: page.NodeID, ordinal: page.PageOrdinal, received: now})
	hold := !proxy.delayed && page.NodeID == proxy.nodeID && page.PageOrdinal == proxy.ordinal
	if hold {
		proxy.delayed, proxy.heldFrom = true, now
		close(proxy.started)
	}
	proxy.mutex.Unlock()
	if hold && !proxy.wait(request.Context()) {
		return
	}
	proxy.engine.ServeHTTP(writer, request)
}

// wait delays the held write and reports whether the client still waits.
func (proxy *pageDelayProxy) wait(ctx context.Context) bool {
	var timer <-chan time.Time
	if proxy.delay > 0 {
		timer = time.After(proxy.delay)
	}
	abandoned := false
	select {
	case <-ctx.Done():
		abandoned = true
	case <-timer:
	}
	proxy.mutex.Lock()
	proxy.heldUntil, proxy.abandoned = time.Now(), abandoned
	proxy.mutex.Unlock()
	return !abandoned
}

// target selects the page write the proxy delays.
func (proxy *pageDelayProxy) target(nodeID uuid.UUID, ordinal uint64) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	proxy.nodeID, proxy.ordinal = nodeID.String(), ordinal
}

// held returns the hold start and end and whether the client abandoned the
// held write. A zero end means the write is still held.
func (proxy *pageDelayProxy) held() (from, until time.Time, abandoned bool) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return proxy.heldFrom, proxy.heldUntil, proxy.abandoned
}

// writesOf returns the page writes of nodeID the proxy received.
func (proxy *pageDelayProxy) writesOf(nodeID uuid.UUID) []delayedPageWrite {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	writes := make([]delayedPageWrite, 0, len(proxy.writes))
	for _, write := range proxy.writes {
		if write.nodeID == nodeID.String() {
			writes = append(writes, write)
		}
	}
	return writes
}

// newDelayedSearchIndex provisions the model and one native index through a
// TLS proxy that delays the first write of page ordinal of nodeID by delay
// (zero waits for the client to abandon it), and records the index as the
// serving index. The typed client reads the engine directly.
func newDelayedSearchIndex(t *testing.T, stores *fdbadapter.Stores, delay time.Duration) (*search.Adapter, *opensearchapi.Client, *pageDelayProxy, string) {
	t.Helper()
	fixture := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
	target, err := url.Parse(fixture.Endpoint)
	if err != nil {
		t.Fatalf("parse engine endpoint: %v", err)
	}
	engineCA := x509.NewCertPool()
	if !engineCA.AppendCertsFromPEM([]byte(fixture.CA)) {
		t.Fatal("engine CA is invalid")
	}
	proxy := &pageDelayProxy{
		engine: &httputil.ReverseProxy{
			Rewrite:   func(outbound *httputil.ProxyRequest) { outbound.SetURL(target) },
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: engineCA, MinVersion: tls.VersionTLS12}},
		},
		nodeID: "", ordinal: 0, delay: delay, started: make(chan struct{}),
		mutex: sync.Mutex{}, delayed: false, heldFrom: time.Time{}, heldUntil: time.Time{}, abandoned: false, writes: nil,
	}
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen for the delay proxy: %v", err)
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
		t.Fatalf("create delayed adapter: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	_, client := openSearchClientsFor(t, fixture)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("provision search model: %v", err)
	}
	index := "node-pages-" + uuid.Must(uuid.NewV7()).String()
	createNativeSearchIndex(t, adapter, client, model, index)
	if err := stores.InitializeSearchIndex(t.Context(), index); err != nil {
		t.Fatalf("record serving search index: %v", err)
	}
	return adapter, client, proxy, index
}
