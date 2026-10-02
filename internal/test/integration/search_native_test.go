package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	opensearch "github.com/opensearch-project/opensearch-go/v4"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/testenv"
)

// nativeSearchMemoryBytes is the model's per-guest memory floor.
const nativeSearchMemoryBytes int64 = 8 << 30

// nativeSearchConfig returns the production adapter settings for one engine.
func nativeSearchConfig(fixture testenv.OpenSearchFixture) search.Config {
	pass := fixture.Password
	return search.Config{
		Endpoint: fixture.Endpoint, CA: fixture.CA, Username: fixture.Username,
		Password: pass, RequestTimeout: 2 * time.Minute, MaxRetries: 1,
	}
}

// openSearchClientsFor returns the production adapter and a typed official
// client built from the same production client settings.
func openSearchClientsFor(t *testing.T, fixture testenv.OpenSearchFixture) (*search.Adapter, *opensearchapi.Client) {
	t.Helper()
	configuration := nativeSearchConfig(fixture)
	adapter, err := search.New(t.Context(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := adapter.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	client, err := opensearch.NewClient(configuration.ClientConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	return adapter, opensearchapi.NewFromClient(client)
}

func newOpenSearchClients(t *testing.T, memoryBytes int64) (*search.Adapter, *opensearchapi.Client) {
	t.Helper()
	return openSearchClientsFor(t, testenv.OpenSearchWithMemory(t, memoryBytes))
}

func nativeSearchClients(t *testing.T) (*search.Adapter, *opensearchapi.Client) {
	t.Helper()
	return newOpenSearchClients(t, nativeSearchMemoryBytes)
}

// createNativeSearchIndex deletes any existing index, provisions the index
// through the production adapter, and deletes it when the test ends.
func createNativeSearchIndex(t *testing.T, adapter *search.Adapter, client *opensearchapi.Client, model search.ModelInfo, index string) search.IndexSpec {
	t.Helper()
	deleteNativeIndex(t, client, index)
	t.Cleanup(func() { deleteNativeIndex(t, client, index) })
	spec := search.IndexSpec{Model: model, MappingVersion: "1", Primaries: 1, RoutingShards: 8, Replicas: 0}
	if err := adapter.EnsureIndex(t.Context(), index, spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

// deleteNativeIndex removes index and its aliases, accepting an absent index.
func deleteNativeIndex(t *testing.T, client *opensearchapi.Client, index string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
	defer cancel()
	response, err := opensearch.Do[json.RawMessage](ctx, client.Client, http.MethodDelete,
		opensearchapi.IndicesDeleteReq{Indices: []string{index}}, nil)
	if err != nil {
		t.Errorf("delete OpenSearch index %s: %v", index, err)
		return
	}
	if response.IsError() && response.StatusCode != http.StatusNotFound {
		t.Errorf("delete OpenSearch index %s: %v", index, opensearch.ParseError(response))
	}
}

func unicodePage4096() string {
	var page strings.Builder
	for position := range 1023 {
		_, _ = fmt.Fprintf(&page, "%04d", position)
	}
	return page.String() + "汉\n"
}

func TestSearchNativeSparse(t *testing.T) {
	adapter, client := nativeSearchClients(t)
	requireNativeJVMHeap(t, client)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if model.ID == "" || model.Name != search.PinnedModel.Name || model.Version != search.PinnedModel.Version {
		t.Fatalf("unexpected deployed model: %+v", model)
	}
	const index = "native-sparse-compatibility"
	createNativeSearchIndex(t, adapter, client, model, index)
	for _, workload := range nativeCompletePageCases() {
		t.Run(workload.name, func(t *testing.T) {
			text := workload.text
			if len(text) != 4096 {
				t.Fatalf("test page contains %d bytes, want 4096", len(text))
			}
			requireBulkSucceeded(t, nativeBulk(t, client, nativeIndexAction(t, index, workload.name, 1, nativePageDocument(t, workload.name, text))))
			source := nativeSource(t, client, index, workload.name)
			encodedSource, err := json.Marshal(source)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("native source case=%s document=%s", workload.name, encodedSource)
			var pageText string
			if err := json.Unmarshal(source["page_text"], &pageText); err != nil {
				t.Fatal(err)
			}
			if pageText != text || len(source["page_text_semantic_info"]) == 0 {
				t.Fatalf("source or native chunks missing: source bytes=%d semantic bytes=%d", len(pageText), len(source["page_text_semantic_info"]))
			}
			requireNativeChunks(t, source["page_text_semantic_info"], text)
			t.Logf("8 GiB native case=%s bytes=%d semantic_bytes=%d", workload.name, len(text), len(source["page_text_semantic_info"]))
		})
	}
}
