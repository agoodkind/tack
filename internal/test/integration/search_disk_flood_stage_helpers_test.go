package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/google/uuid"
)

// readOnlyAllowDeleteSetting is the index block that the OpenSearch disk
// threshold monitor sets at the flood stage and clears below the high
// watermark.
const readOnlyAllowDeleteSetting = "index.blocks.read_only_allow_delete"

// waitForBlock reads the serving index settings until the
// read_only_allow_delete block is set when blocked is true, or absent when
// blocked is false. The test only reads the setting.
func waitForBlock(t *testing.T, fixture queryFixture, blocked bool) {
	t.Helper()
	clusterEventually(t, fmt.Sprintf("read %s %t on %s", readOnlyAllowDeleteSetting, blocked, fixture.Index), func() error {
		path := "/" + url.PathEscape(fixture.Index) + "/_settings/" + readOnlyAllowDeleteSetting + "?flat_settings=true"
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
		clusterRequire(t, "build the settings request", err)
		response, err := fixture.Client.Client.Perform(request)
		if err != nil {
			return clusterFailure("read the index block", err)
		}
		defer func() { _ = response.Body.Close() }()
		var indices map[string]struct {
			Settings map[string]string `json:"settings"`
		}
		if err := json.NewDecoder(response.Body).Decode(&indices); err != nil {
			return clusterFailure("decode the index block", err)
		}
		value, set := indices[fixture.Index].Settings[readOnlyAllowDeleteSetting]
		if (set && value == "true") != blocked {
			return fmt.Errorf("%s on %s = %q (set %t)", readOnlyAllowDeleteSetting, fixture.Index, value, set)
		}
		return nil
	})
}

// requireIncludedText requires the stored view of nodeID to contain included
// as its included text.
func requireIncludedText(t *testing.T, fixture queryFixture, kind opaqueKind, nodeID uuid.UUID, included string) {
	t.Helper()
	view, err := fixture.Stores.Views.Get(t.Context(), nodeID)
	if err != nil || view == nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}
	if stored := view.Props[kind.IncludedKey]; !bytes.Equal(stored, mustJSON(included)) {
		t.Fatalf("node %s included text = %s, want %q", nodeID, stored, included)
	}
}
