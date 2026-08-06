package datagen

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func TestDiscoverNodesSkipsUnresolvableItem(t *testing.T) {
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload rpcRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload.Params.Name {
		case "tack_list_modules":
			writeDiscoveryListItems(writer, "modules", []collectionItem{
				{Reference: "Q0101-1", Name: "Existing module"},
				{Reference: "foreign-reference", Name: "Foreign module"},
			})
		case "tack_get_module":
			writeDiscoveryNode(writer, deterministicUUID("module").String(), "", 0)
		default:
			http.Error(writer, "unexpected tool "+payload.Params.Name, http.StatusBadRequest)
		}
	})
	nodes, err := discoverScopedNodes(
		t.Context(),
		NewDriver(testGraph(handler), false, 777),
		"token",
		"qa-777-o01-w01",
		"Q0101",
		"Q0101",
		"module",
		"modules",
		nil,
	)
	if err != nil {
		t.Fatalf("discoverNodes() error = %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "Existing module" {
		t.Fatalf("discoverNodes() = %#v, want existing module only", nodes)
	}
	logs := output.String()
	if !strings.Contains(logs, "qa.datagen.soak.discovery_skipped") {
		t.Fatalf("logs = %q, want discovery skip warning", logs)
	}
	if !strings.Contains(logs, "foreign-reference") {
		t.Fatalf("logs = %q, want skipped reference", logs)
	}
}
