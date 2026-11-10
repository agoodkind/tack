package integration_test

import (
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/testenv"
)

func TestServiceSecretOpenSearchRoles(t *testing.T) {
	directory := os.Getenv("TACK_TEST_SEARCH_SECURITY_DIRECTORY")
	engine := testenv.OpenSearchWithSecurityFiles(t, directory)
	maintenance := secretSearchAdapter(t, engine, "tack_maintenance", readFixturePassword(t, directory, "OPENSEARCH_MAINTENANCE_PASSWORD"))
	runtimePassword := readFixturePassword(t, directory, "OPENSEARCH_PASSWORD")
	runtime := secretSearchAdapter(t, engine, "tack_runtime", runtimePassword)
	healthPassword := readFixturePassword(t, directory, "OPENSEARCH_HEALTH_PASSWORD")

	model, err := maintenance.Provision(t.Context())
	if err != nil {
		logSecretSearchTaskFailures(t, engine)
		t.Fatalf("maintenance could not provision the pinned model: %v", err)
	}
	index := "node-pages-role-" + uuid.NewString()
	if err := runtime.CreateReplacement(t.Context(), index, 1, 24, 0); err != nil {
		t.Fatalf("runtime could not create a replacement with the existing model: %v", err)
	}
	if err := runtime.WaitGreen(t.Context(), index, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetAlias(t.Context(), "node-pages", index); err != nil {
		t.Fatalf("runtime could not switch its public alias: %v", err)
	}
	if err := runtime.SetWriteBlock(t.Context(), index, false); err != nil {
		t.Fatalf("runtime could not update its index settings: %v", err)
	}
	if err := runtime.Refresh(t.Context(), index); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.IndexRetention(t.Context(), index); err != nil {
		t.Fatalf("runtime could not inspect retention: %v", err)
	}
	verifySecretSearchPageOperations(t, runtime, index)
	for _, request := range []struct {
		method string
		path   string
		body   map[string]string
		status int
	}{
		{http.MethodPost, "/_plugins/_ml/models/_register", map[string]string{
			"name": search.PinnedModel.Name, "version": search.PinnedModel.Version,
			"model_format": "TORCH_SCRIPT", "function_name": "SPARSE_ENCODING",
		}, http.StatusForbidden},
		{http.MethodPut, "/unrelated-role-fixture", nil, http.StatusForbidden},
		{http.MethodGet, "/_plugins/_security/api/internalusers", nil, http.StatusForbidden},
	} {
		if status := secretSearchRequest(t, engine, "tack_runtime", runtimePassword, request.method, request.path, request.body); status != request.status {
			t.Fatalf("runtime %s %s returned %d, expected %d", request.method, request.path, status, request.status)
		}
	}
	verifySecretSearchConnectorDenial(t, engine, runtimePassword)
	if _, err := maintenance.LocalModel(t.Context(), model.ID); err != nil {
		t.Fatalf("maintenance could not verify the local model and connector list: %v", err)
	}
	if status := secretSearchRequest(t, engine, "tack_health", healthPassword, http.MethodGet, "/_cluster/health", nil); status != http.StatusOK {
		t.Fatalf("health readiness returned %d", status)
	}
	if status := secretSearchRequest(t, engine, "tack_health", healthPassword, http.MethodPost, "/"+index+"/_search", nil); status != http.StatusForbidden {
		t.Fatalf("health document search returned %d", status)
	}
	if err := runtime.DeleteIndex(t.Context(), index); err != nil {
		t.Fatalf("runtime could not retire its own index: %v", err)
	}
}

func secretSearchAdapter(t *testing.T, engine testenv.OpenSearchFixture, username, password string) *search.Adapter {
	t.Helper()
	adapter, err := search.New(t.Context(), search.Config{
		Endpoint: engine.Endpoint, CA: engine.CA, Username: username, Password: password,
		RequestTimeout: time.Minute, MaxRetries: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close(t.Context()) })
	return adapter
}
