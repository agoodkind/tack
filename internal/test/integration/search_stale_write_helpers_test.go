package integration

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
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
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/testenv"
)

const staleDocumentID = "stale-write-page"

// staleEngine is the production adapter and a typed client on one real engine.
type staleEngine struct {
	fixture testenv.OpenSearchFixture
	adapter *search.Adapter
	client  *opensearchapi.Client
	model   search.ModelInfo
}

func newStaleEngine(t *testing.T) staleEngine {
	t.Helper()
	fixture := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
	adapter, client := openSearchClientsFor(t, fixture)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("provision the search model: %v", err)
	}
	return staleEngine{fixture: fixture, adapter: adapter, client: client, model: model}
}

func (engine staleEngine) newIndex(t *testing.T, name string) string {
	t.Helper()
	createNativeSearchIndex(t, engine.adapter, engine.client, engine.model, name)
	return name
}

func staleWork(nodeID uuid.UUID, generation int64, target, mirror string) searchdomain.Work {
	return searchdomain.Work{
		OrgID: uuid.Nil, NodeID: nodeID, Generation: generation, Revision: "1", Projection: "projection",
		Cursor: "", Ordinal: 0, Phase: searchdomain.PhasePages, Deleted: false, EnqueuedAt: time.Time{},
		Owner: "stale-write-test", LeaseUntil: time.Time{}, Class: searchdomain.WorkClassLive,
		Target: target, Mirror: mirror,
	}
}

func staleContent(work searchdomain.Work, text, accessKey string) searchdomain.WriteIntent {
	access := node.SearchAccess{Versions: []string{"v1"}, Keys: []string{accessKey}, Generation: work.Generation}
	page := node.ContentPage{
		NodeID: work.NodeID, NodeType: "stale-node", Revision: "1", ProjectionVersion: "projection",
		Name: "stale page", Text: text, Access: access, Ordinal: 0, OverlapBytes: 0, NextCursor: "", Done: true,
	}
	return searchdomain.WriteIntent{Work: work, Page: page, DocumentID: staleDocumentID}
}

func staleIssuedDocuments() []searchdomain.IssuedDocument {
	return []searchdomain.IssuedDocument{{DocumentID: staleDocumentID, Revision: 1, Ordinal: 0, Projection: "projection"}}
}

func staleAccess(work searchdomain.Work, accessKey string) searchdomain.AccessIntent {
	access := node.SearchAccess{Versions: []string{"v1"}, Keys: []string{accessKey}, Generation: work.Generation}
	return searchdomain.AccessIntent{Work: work, Documents: staleIssuedDocuments(), Access: access}
}

func staleRetirement(work searchdomain.Work) searchdomain.RetirementIntent {
	return searchdomain.RetirementIntent{Work: work, Documents: staleIssuedDocuments()}
}

// rawStaleSource returns the stored _source bytes of the test page.
func rawStaleSource(t *testing.T, client *opensearchapi.Client, index string) []byte {
	t.Helper()
	response, err := client.Document.Get(t.Context(), opensearchapi.DocumentGetReq{Index: index, DocumentID: staleDocumentID})
	if err != nil || !response.Found {
		t.Fatalf("read page in %s: found %t err %v", index, response.Found, err)
	}
	return response.Source
}

// requireUnchanged requires the stored source to equal before byte for byte.
func requireUnchanged(t *testing.T, client *opensearchapi.Client, index string, before []byte, step string) {
	t.Helper()
	if after := rawStaleSource(t, client, index); !bytes.Equal(before, after) {
		t.Fatalf("%s changed the stored source in %s:\nbefore %s\nafter  %s", step, index, before, after)
	}
}

// requireStoredPage requires the stored generation, text, and access key.
func requireStoredPage(t *testing.T, client *opensearchapi.Client, index string, generation int64, text, accessKey string) {
	t.Helper()
	var stored struct {
		SearchGeneration int64  `json:"search_generation"`
		PageText         string `json:"page_text"`
		Retired          bool   `json:"retired"`
		Access           struct {
			Keys []string `json:"keys"`
		} `json:"access"`
	}
	if err := json.Unmarshal(rawStaleSource(t, client, index), &stored); err != nil {
		t.Fatalf("decode page in %s: %v", index, err)
	}
	if stored.SearchGeneration != generation || stored.PageText != text || stored.Retired ||
		len(stored.Access.Keys) != 1 || stored.Access.Keys[0] != accessKey {
		t.Fatalf("page in %s = %+v, want generation %d text %q access %q", index, stored, generation, text, accessKey)
	}
}

// staleProxy counts the requests of a TLS reverse proxy to the real engine.
type staleProxy struct {
	mutex        sync.Mutex
	mgetRequests int
	bulkRequests int
}

func (proxy *staleProxy) counts() (int, int) {
	proxy.mutex.Lock()
	defer proxy.mutex.Unlock()
	return proxy.mgetRequests, proxy.bulkRequests
}

// newStaleProxyAdapter returns a production adapter that sends every request
// to a local TLS proxy. The proxy forwards each request to the engine. Before
// it forwards the first bulk request it runs inject, which writes to the
// engine directly.
func newStaleProxyAdapter(t *testing.T, fixture testenv.OpenSearchFixture, inject func() error) (*search.Adapter, *staleProxy) {
	t.Helper()
	target, err := url.Parse(fixture.Endpoint)
	if err != nil {
		t.Fatalf("parse engine endpoint: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(fixture.CA))
	forward := httputil.NewSingleHostReverseProxy(target)
	forward.Transport = &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	proxy := &staleProxy{mutex: sync.Mutex{}, mgetRequests: 0, bulkRequests: 0}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxy.mutex.Lock()
		isBulk := strings.HasSuffix(request.URL.Path, "/_bulk")
		firstBulk := isBulk && proxy.bulkRequests == 0
		if isBulk {
			proxy.bulkRequests++
		}
		if strings.HasSuffix(request.URL.Path, "/_mget") {
			proxy.mgetRequests++
		}
		proxy.mutex.Unlock()
		if firstBulk {
			if err := inject(); err != nil {
				t.Errorf("inject the concurrent write: %v", err)
				http.Error(writer, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		request.Host = target.Host
		forward.ServeHTTP(writer, request)
	})
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Fatalf("listen for the proxy: %v", err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.StartTLS()
	t.Cleanup(server.Close)
	authority := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	configuration := nativeSearchConfig(fixture)
	configuration.Endpoint, configuration.CA = server.URL, string(authority)
	adapter, err := search.New(t.Context(), configuration)
	if err != nil {
		t.Fatalf("create the adapter behind the proxy: %v", err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	return adapter, proxy
}
