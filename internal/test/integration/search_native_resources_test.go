package integration

import (
	"slices"
	"strings"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

func TestSearchNativeClientUsesOnlyTheConfiguredEndpoint(t *testing.T) {
	fixture := testenv.OpenSearch(t)
	adapter, _ := openSearchClientsFor(t, fixture)
	if err := adapter.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ServerVersion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Provision(t.Context()); err != nil {
		t.Fatal(err)
	}
	endpoints, err := adapter.Endpoints(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{strings.TrimRight(fixture.Endpoint, "/")}
	if !slices.Equal(endpoints, want) {
		t.Fatalf("official client used endpoints %q, want only %q", endpoints, want)
	}
}
