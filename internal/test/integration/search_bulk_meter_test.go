package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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

// bulkRecord is the target index and body length of one bulk request.
type bulkRecord struct {
	index string
	bytes int
}

// bulkMeter forwards every request to the engine and records each bulk
// request. A page write sends one bulk request with one encoded page to the
// target index, and one more to the mirror index during a replacement.
type bulkMeter struct {
	engine  *httputil.ReverseProxy
	mutex   sync.Mutex
	records []bulkRecord
}

func (meter *bulkMeter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if strings.HasSuffix(request.URL.Path, "/_bulk") {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "read bulk body: "+err.Error(), http.StatusBadRequest)
			return
		}
		meter.mutex.Lock()
		index := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/"), "/_bulk")
		meter.records = append(meter.records, bulkRecord{index: index, bytes: len(body)})
		meter.mutex.Unlock()
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
	}
	meter.engine.ServeHTTP(writer, request)
}

// take returns the bulk requests recorded since the last call.
func (meter *bulkMeter) take() []bulkRecord {
	meter.mutex.Lock()
	defer meter.mutex.Unlock()
	records := meter.records
	meter.records = nil
	return records
}

// newMeteredSearchIndex provisions the model and one native index through a
// TLS proxy that records bulk body lengths, and records the index as the
// serving index. The production adapter sends every request through the
// proxy; the typed client reads the engine directly.
func newMeteredSearchIndex(t *testing.T, stores *fdbadapter.Stores) (*search.Adapter, *opensearchapi.Client, *bulkMeter, string) {
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
	meter := &bulkMeter{engine: &httputil.ReverseProxy{
		Rewrite:   func(outbound *httputil.ProxyRequest) { outbound.SetURL(target) },
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: engineCA, MinVersion: tls.VersionTLS12}},
	}}
	listener, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen for the bulk meter: %v", err)
	}
	server := httptest.NewUnstartedServer(meter)
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
		t.Fatalf("create metered adapter: %v", err)
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
	meter.take()
	return adapter, client, meter, index
}
