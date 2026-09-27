package integration

import (
	"net/http"
	"strings"
	"testing"

	"github.com/opensearch-project/opensearch-go/v4/opensearchapi"
)

// TestSearchProjectionReadiness requires each audited search command to
// return an error before it sends a request to OpenSearch while one stored
// property definition lacks a search declaration. Provisioning must succeed
// after the declaration is restored.
func TestSearchProjectionReadiness(t *testing.T) {
	env := SetupTestEnv(t)
	control := newSearchControl(t)
	definitions, err := env.Stores.PropertyDefs.List(env.Ctx, env.OrgID)
	if err != nil || len(definitions) == 0 || definitions[0].Search == nil {
		t.Fatalf("list declared definitions: count = %d, error = %v", len(definitions), err)
	}
	declared := *definitions[0]
	missing := declared
	missing.Search = nil
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &missing); err != nil {
		t.Fatalf("clear projection: %v", err)
	}
	for _, command := range []string{"provision", "verify"} {
		err := control.run("ops", "search", command)
		if err == nil || !strings.Contains(err.Error(), "missing search projections for 1 definitions") {
			t.Fatalf("%s with a missing projection: error = %v", command, err)
		}
		if searchIndexExists(t, control, searchControlIndex) {
			t.Fatalf("%s created %s before every definition declared search", command, searchControlIndex)
		}
	}
	if err := env.Stores.PropertyDefs.Set(env.Ctx, &declared); err != nil {
		t.Fatalf("restore projection: %v", err)
	}
	if err := control.run("ops", "search", "provision"); err != nil {
		t.Fatalf("provision after every definition declared search: %v", err)
	}
}

func searchIndexExists(t *testing.T, control searchControl, index string) bool {
	t.Helper()
	response, err := control.client.Indices.Exists(t.Context(), opensearchapi.IndicesExistsReq{Indices: []string{index}})
	if response != nil && response.StatusCode == http.StatusNotFound {
		return false
	}
	if err != nil {
		t.Fatalf("check OpenSearch index %s: %v", index, err)
	}
	return response.StatusCode == http.StatusOK
}
