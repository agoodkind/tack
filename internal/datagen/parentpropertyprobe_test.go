package datagen

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestParentPropertyProbeEncodesIssueReference(t *testing.T) {
	t.Parallel()

	const reference = `PROJ::a"b\c`
	var matches int
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var call rpcRequest
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			t.Fatal(err)
		}

		var value *string
		if err := json.Unmarshal(call.Params.Arguments.Properties["parent_id"], &value); err != nil {
			t.Fatal(err)
		}
		if value != nil {
			if *value != reference {
				t.Errorf("%s parent_id = %q, want %q", call.Params.Name, *value, reference)
			}
			matches++
		}

		http.Error(w, "properties.parent_id is not declared", http.StatusBadRequest)
	})
	generator := &Generator{driver: NewDriver(testGraph(handler), false, 1)}
	workspace := WorkspaceIdentity{Slug: "main", Actors: []Actor{{Token: "token"}}}

	if err := generator.probeParentPropertyRefused(t.Context(), workspace, "PROJ", reference); err != nil {
		t.Fatal(err)
	}
	if matches != 2 {
		t.Fatalf("received %d string parent references, want 2", matches)
	}
}
