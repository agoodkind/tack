package integration_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/adapters/search"
	"goodkind.io/tack/internal/testenv"
)

const (
	sdkCompatibilityDiskBytes   int64 = 32 * 1024 * 1024 * 1024
	sdkCompatibilityMemoryBytes int64 = 8 * 1024 * 1024 * 1024
)

// TestSearchSDKCompatibility checks the public adapter against a pinned engine
// with its own data filesystem and the existing disk-space gate.
func TestSearchSDKCompatibility(t *testing.T) {
	engine := testenv.DisposableOpenSearch(t, testenv.DisposableOpenSearchOptions{
		MemoryBytes: sdkCompatibilityMemoryBytes, DataBytes: sdkCompatibilityDiskBytes,
	})
	fixture := engine.Fixture
	adapter := secretSearchAdapter(t, fixture, fixture.Username, fixture.Password)
	if err := adapter.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	model, err := adapter.Provision(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	index := "node-pages-sdk-" + uuid.NewString()
	t.Cleanup(func() {
		if err := adapter.DeleteIndex(context.WithoutCancel(t.Context()), index); err != nil {
			t.Error(err)
		}
	})
	spec := search.IndexSpec{Model: model, MappingVersion: search.MappingVersion, Primaries: 1, RoutingShards: 24, Replicas: 0}
	if err := adapter.EnsureIndex(t.Context(), index, spec); err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetReplicas(t.Context(), index, 0); err != nil {
		t.Fatal(err)
	}
	if err := adapter.SetAlias(t.Context(), search.PublicAlias, index); err != nil {
		t.Fatal(err)
	}
	if target, err := adapter.AliasTarget(t.Context(), search.PublicAlias); err != nil || target != index {
		t.Fatalf("public alias selects %q, expected %q: %v", target, index, err)
	}
	if err := adapter.VerifyIndex(t.Context(), index, spec); err != nil {
		t.Fatal(err)
	}
	verifySecretSearchPageOperations(t, adapter, index)
	retention, err := adapter.IndexRetention(t.Context(), index)
	if err != nil || retention.RetiredPages != 1 || retention.PrimaryBytes <= 0 {
		t.Fatalf("retention reports %+v, expected one retired page and stored bytes: %v", retention, err)
	}
	endpoints, err := adapter.Endpoints(t.Context())
	if err != nil || !slices.Equal(endpoints, []string{strings.TrimRight(fixture.Endpoint, "/")}) {
		t.Fatalf("client endpoints are %v, expected only the configured endpoint: %v", endpoints, err)
	}
}
