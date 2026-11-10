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
	if err := adapter.Ping(WithTestOperation(t, t.Context())); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ServerVersion(WithTestOperation(t, t.Context())); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Provision(WithTestOperation(t, t.Context())); err != nil {
		t.Fatal(err)
	}
	endpoints, err := adapter.Endpoints(WithTestOperation(t, t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{strings.TrimRight(fixture.Endpoint, "/")}
	if !slices.Equal(endpoints, want) {
		t.Fatalf("official client used endpoints %q, want only %q", endpoints, want)
	}
}
