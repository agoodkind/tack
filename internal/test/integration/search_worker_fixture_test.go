package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/domain/node"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/service"
)

// searchPageSource is one indexed page document read back from OpenSearch.
type searchPageSource struct {
	ID                string  `json:"-"`
	NodeID            string  `json:"node_id"`
	NodeType          string  `json:"node_type"`
	Name              string  `json:"name"`
	NodeRevision      string  `json:"node_revision"`
	ProjectionVersion string  `json:"projection_version"`
	PageOrdinal       uint64  `json:"page_ordinal"`
	PageText          *string `json:"page_text"`
	Retired           bool    `json:"retired"`
	// SearchGeneration decodes a number or a numeric string. The partial
	// failure test stores the generation of one page as a string.
	SearchGeneration json.Number     `json:"search_generation"`
	Access           nativeAccess    `json:"access"`
	Semantic         json.RawMessage `json:"page_text_semantic_info"`
}

// pageGeneration returns the stored search generation of page as a number.
func pageGeneration(t *testing.T, page searchPageSource) int64 {
	t.Helper()
	generation, err := page.SearchGeneration.Int64()
	if err != nil {
		t.Fatalf("parse page %s search generation %q: %v", page.ID, page.SearchGeneration, err)
	}
	return generation
}

func searchWorkerSettings(pageBytes int) config.SearchWorkerSettings {
	return config.SearchWorkerSettings{
		PageBytes: pageBytes, MaxPages: 32, MaxBytes: 5 << 20, SliceBudget: 2 * time.Second,
		OperationTimeout: 10 * time.Second, Lease: 30 * time.Second, IdleInterval: 50 * time.Millisecond,
		Concurrency: 1, ClassWeights: map[string]int{"live": 1, "access": 1, "cleanup": 1, "rescan": 1},
	}
}

// newSearchWorker builds the production worker over the production stores
// and the production OpenSearch adapter.
func newSearchWorker(t *testing.T, stores *fdbadapter.Stores, adapter *search.Adapter, source clock.Clock, settings config.SearchWorkerSettings) *service.SearchWorker {
	t.Helper()
	policies := stores.SearchPolicySet()
	worker, err := service.NewSearchWorker(service.SearchWorkerPorts{
		Store: stores.SearchWork(source), Reader: stores.SearchContent(policies),
		Access: stores.SearchAccess(policies), Writer: adapter,
	}, source, settings, 0)
	if err != nil {
		t.Fatalf("create search worker: %v", err)
	}
	return worker
}

// runSearchWorkerUntilIdle runs slices until no class has claimable work.
func runSearchWorkerUntilIdle(t *testing.T, worker *service.SearchWorker) {
	t.Helper()
	for range 500 {
		claimed, err := worker.RunSlice(t.Context())
		if err != nil {
			t.Fatalf("run search worker slice: %v", err)
		}
		if !claimed {
			return
		}
	}
	t.Fatal("search work did not converge within 500 slices")
}

// newSearchIndex provisions the model and one native index, and records it
// as the serving index in FoundationDB.
func newSearchIndex(t *testing.T, stores *fdbadapter.Stores) (*search.Adapter, *opensearchapi.Client, search.ModelInfo, string) {
	t.Helper()
	adapter, client := nativeSearchClients(t)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatalf("provision search model: %v", err)
	}
	index := "pages-" + uuid.Must(uuid.NewV7()).String()
	createNativeSearchIndex(t, adapter, client, model, index)
	if err := stores.InitializeSearchIndex(t.Context(), index); err != nil {
		t.Fatalf("record serving search index: %v", err)
	}
	return adapter, client, model, index
}

// searchNodePages refreshes index and returns the node's documents with the
// requested retirement state, in page order.
func searchNodePages(t *testing.T, client *opensearchapi.Client, index string, nodeID uuid.UUID, retired bool) []searchPageSource {
	t.Helper()
	if _, err := client.Indices.Refresh(t.Context(), &opensearchapi.IndicesRefreshReq{Index: []string{index}}); err != nil {
		t.Fatalf("refresh %s: %v", index, err)
	}
	query := fmt.Sprintf(`{"size":1000,"query":{"bool":{"filter":[{"term":{"node_id":%q}},{"term":{"retired":%t}}]}},"sort":[{"page_ordinal":"asc"}]}`, nodeID.String(), retired)
	response, err := client.Search(t.Context(), &opensearchapi.SearchReq{Indices: []string{index}, Body: strings.NewReader(query)})
	if err != nil {
		t.Fatalf("search pages of node %s: %v", nodeID, err)
	}
	pages := make([]searchPageSource, 0, len(response.Hits.Hits))
	for _, hit := range response.Hits.Hits {
		var page searchPageSource
		if err := json.Unmarshal(hit.Source, &page); err != nil {
			t.Fatalf("decode page %s: %v", hit.ID, err)
		}
		page.ID = hit.ID
		pages = append(pages, page)
	}
	return pages
}

// readSearchPages reads every page of the node's current revision through
// the production reader.
func readSearchPages(t *testing.T, stores *fdbadapter.Stores, nodeID uuid.UUID, pageBytes int) []node.ContentPage {
	t.Helper()
	reader := stores.SearchContent(stores.SearchPolicySet())
	request := searchdomain.ContentRequest{NodeID: nodeID, Cursor: "", ProjectionConfig: "", AccessVersions: nil, MaxBytes: pageBytes, SearchGeneration: 0}
	pages := make([]node.ContentPage, 0, 8)
	for range 10_000 {
		page, err := reader.Content(t.Context(), request)
		if err != nil {
			t.Fatalf("read page %d of node %s: %v", len(pages), nodeID, err)
		}
		pages = append(pages, page)
		if page.Done {
			return pages
		}
		request.Cursor = page.NextCursor
	}
	t.Fatalf("node %s did not finish within 10000 pages", nodeID)
	return nil
}

// uniqueSearchText joins pages after removing each page's overlap.
func uniqueSearchText(pages []node.ContentPage) string {
	var unique strings.Builder
	for _, page := range pages {
		unique.WriteString(page.Text[page.OverlapBytes:])
	}
	return unique.String()
}

// requireIndexedPages requires one active document per reader page with the
// same text, revision, projection, and ordinal. Each document generation is
// at least minimum and equals its access generation.
func requireIndexedPages(t *testing.T, documents []searchPageSource, pages []node.ContentPage, minimum int64) {
	t.Helper()
	if len(documents) != len(pages) {
		t.Fatalf("node has %d active documents, want %d pages", len(documents), len(pages))
	}
	for number, document := range documents {
		page := pages[number]
		if document.PageText == nil || *document.PageText != page.Text || document.PageOrdinal != page.Ordinal ||
			document.NodeRevision != page.Revision || document.ProjectionVersion != page.ProjectionVersion {
			t.Fatalf("document %d does not match reader page %d", number, page.Ordinal)
		}
		generation, err := document.SearchGeneration.Int64()
		if err != nil || generation < minimum || int64(document.Access.Generation) != generation {
			t.Fatalf("document %d generation = %s, access generation %d, want at least %d", number, document.SearchGeneration, document.Access.Generation, minimum)
		}
	}
}
