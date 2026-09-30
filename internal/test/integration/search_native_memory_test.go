package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/testenv"
)

// TestSearchNativeFourGiBCircuitBreaker preserves the rejected resource
// configuration with the same complete-page workload as the 8 GiB control.
func TestSearchNativeFourGiBCircuitBreaker(t *testing.T) {
	engine := testenv.DisposableOpenSearch(t, 4<<30)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
		defer cancel()
		if err := engine.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	adapter, client := openSearchClientsFor(t, engine)
	model, err := adapter.Provision(t.Context())
	if err != nil {
		requireMemoryCircuitBreaker(t, err.Error())
		return
	}
	const index = "native-four-gib-regression"
	createNativeSearchIndex(t, adapter, client, model, index)
	items := nativeBulk(t, client, nativeIndexAction(t, index, "page-1", 1, nativePageDocument(t, "page-1", unicodePage4096())))
	if len(items) != 1 || items[0].Error == nil || items[0].Status < 300 {
		t.Fatalf("4 GiB complete-page workload returned %+v, want a memory circuit-breaker rejection", items)
	}
	requireMemoryCircuitBreaker(t, items[0].Error.Reason)
}

func requireMemoryCircuitBreaker(t *testing.T, message string) {
	t.Helper()
	normalized := strings.ToLower(message)
	if !strings.Contains(normalized, "memory") || !strings.Contains(normalized, "circuit breaker") {
		t.Fatalf("4 GiB workload failed without the accepted memory circuit breaker: %s", message)
	}
	t.Logf("4 GiB workload rejected by memory circuit breaker: %s", message)
}
