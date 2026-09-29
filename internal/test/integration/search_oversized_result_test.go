package integration

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

const (
	// oversizedResponseBytes is the smallest response budget the settings
	// accept.
	oversizedResponseBytes = 1024
	// oversizedTypeKeyPadding lengthens one node type key. A summary with
	// that type and a full-length name exceeds the budget of an empty
	// response.
	oversizedTypeKeyPadding = 200
	// oversizedNameRepeats makes the summary name longer than the name bound.
	oversizedNameRepeats = 40
	// oversizedCorpusNodes is the number of ordinary matching nodes.
	oversizedCorpusNodes = 10
)

// TestSearchWithholdsOversizedResult requires a traversal to complete, return
// every ordinary matching node exactly once, and never return a matching node
// with a summary above the budget of an empty response.
func TestSearchWithholdsOversizedResult(t *testing.T) {
	fixture := newQueryFixture(t, defaultQueryOptions())
	workspace := fixture.Workspaces[0]
	entryID := entryPoint(t, fixture, workspace)
	wideTypeKey := opaqueSearchKey("n") + strings.Repeat("w", oversizedTypeKeyPadding)
	wide := putOpaqueKindWithTypeKey(t, fixture, workspace.OrgID, wideTypeKey)
	oversized := putOpaqueNode(t, fixture, wide, entryID, strings.Repeat("Glacier ", oversizedNameRepeats), "glacier signal", "excluded")
	kind := putOpaqueKind(t, fixture, workspace.OrgID)
	nodes := make([]uuid.UUID, 0, oversizedCorpusNodes)
	for range oversizedCorpusNodes {
		nodes = append(nodes, putOpaqueNode(t, fixture, kind, entryID, "Glacier node", "glacier signal", "excluded"))
	}
	drainSearchWork(t, fixture.Worker, 4*oversizedCorpusNodes+100)
	searcher := queryService(t, fixture, oversizedResponseBytes, 15*time.Minute, 2*time.Hour)
	request := serviceRequest(t, fixture, "glacier signal")
	returned := make([]uuid.UUID, 0, len(nodes))
	for range 50 {
		page, err := searcher.Search(t.Context(), request)
		if err != nil {
			t.Fatalf("search page: %v", err)
		}
		for _, result := range page.Results {
			returned = append(returned, result.ID)
		}
		if page.Complete {
			if slices.Contains(returned, oversized) {
				t.Fatalf("search returned node %s with a summary above the %d-byte response", oversized, oversizedResponseBytes)
			}
			requireExactlyOnce(t, returned, nodes)
			return
		}
		request = continued(request, page)
	}
	t.Fatal("traversal with an oversized result did not complete")
}
