package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
)

const (
	// readOnlyAllowDeleteSetting is the index block that the OpenSearch disk
	// threshold monitor sets at the flood stage and clears below the high
	// watermark.
	readOnlyAllowDeleteSetting = "index.blocks.read_only_allow_delete"
	// infoIntervalSetting is the disk-usage refresh interval that
	// DisposableOpenSearch sets to its 10 s minimum.
	infoIntervalSetting = "cluster.info.update.interval"
	infoIntervalValue   = "10s"
	// mlDiskThresholdSetting is the ML Commons free-space floor that Configs
	// sets to 1gb on every Tack search member and DisposableOpenSearch sets
	// to the same value.
	mlDiskThresholdSetting = "plugins.ml_commons.disk_free_space_threshold"
	mlDiskThresholdValue   = "1gb"
)

// flatSettings is the settings object of one index or node with
// flat_settings=true.
type flatSettings struct {
	Settings map[string]string `json:"settings"`
}

// waitForBlock reads the serving index settings until the
// read_only_allow_delete block is set when blocked is true, or absent when
// blocked is false. The test only reads the setting.
func waitForBlock(t *testing.T, fixture queryFixture, blocked bool) {
	t.Helper()
	clusterEventually(t, fmt.Sprintf("read %s %t on %s", readOnlyAllowDeleteSetting, blocked, fixture.Index), func() error {
		body, err := readEngine(t, fixture, "/"+url.PathEscape(fixture.Index)+"/_settings/"+readOnlyAllowDeleteSetting+"?flat_settings=true")
		if err != nil {
			return err
		}
		var indices map[string]flatSettings
		if err := json.Unmarshal(body, &indices); err != nil {
			return clusterFailure("decode the index block", err)
		}
		value, set := indices[fixture.Index].Settings[readOnlyAllowDeleteSetting]
		if (set && value == "true") != blocked {
			return fmt.Errorf("%s on %s = %q (set %t)", readOnlyAllowDeleteSetting, fixture.Index, value, set)
		}
		return nil
	})
}

// requireDiskSettings requires every node to report the 10 s disk-usage
// refresh interval and the 1gb ML Commons disk threshold that the engine
// environment sets.
func requireDiskSettings(t *testing.T, fixture queryFixture) {
	t.Helper()
	// flat_settings=true returns each setting under its dotted key, and a
	// dotted filter_path matches nested objects only. The read keeps every
	// node setting.
	body, err := readEngine(t, fixture, "/_nodes/settings?flat_settings=true&filter_path=nodes.*.settings")
	clusterRequire(t, "read node settings", err)
	// Each value decodes as raw JSON. Under flat_settings a list setting,
	// such as plugins.security.nodes_dn, is a JSON array.
	var settings struct {
		Nodes map[string]struct {
			Settings map[string]json.RawMessage `json:"settings"`
		} `json:"nodes"`
	}
	clusterRequire(t, "decode node settings", json.Unmarshal(body, &settings))
	if len(settings.Nodes) == 0 {
		t.Fatalf("node settings list no node: %s", body)
	}
	wanted := map[string]json.RawMessage{
		infoIntervalSetting:    mustJSON(infoIntervalValue),
		mlDiskThresholdSetting: mustJSON(mlDiskThresholdValue),
	}
	for nodeID, node := range settings.Nodes {
		for setting, want := range wanted {
			if value := node.Settings[setting]; !bytes.Equal(value, want) {
				t.Fatalf("node %s %s = %s, want %s", nodeID, setting, value, want)
			}
		}
	}
}

// readEngine returns the body of one GET request to the engine.
func readEngine(t *testing.T, fixture queryFixture, path string) ([]byte, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, path, nil)
	clusterRequire(t, "build the engine request", err)
	response, err := fixture.Client.Client.Perform(request)
	if err != nil {
		return nil, clusterFailure("read "+path, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	return body, clusterFailure("read the body of "+path, err)
}

// createFloodProject creates one project named name through the
// tack_create_project MCP tool and returns its ID.
func createFloodProject(t *testing.T, fixture queryFixture, prefix, name string) uuid.UUID {
	t.Helper()
	identifier := prefix + strconv.FormatInt(clock.Now().UnixNano()%1_000_000, 10)
	created := fixture.Harness.Call(t, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: fixture.Harness.Workspace, Name: name,
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))},
	})
	nodeID, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("parse created project id %q: %v", created.RawID(), err)
	}
	return nodeID
}

// renameFloodProject renames one project through the tack_update_project MCP
// tool.
func renameFloodProject(t *testing.T, fixture queryFixture, nodeID uuid.UUID, name string) {
	t.Helper()
	fixture.Harness.Call(t, "tack_update_project", datagen.ToolArguments{
		WorkspaceReference: fixture.Harness.Workspace, NodeID: nodeID.String(), Name: name,
	})
}

// requireStoredName requires the FoundationDB view of nodeID to have name.
func requireStoredName(t *testing.T, fixture queryFixture, nodeID uuid.UUID, name string) {
	t.Helper()
	view, err := fixture.Stores.Views.Get(t.Context(), nodeID)
	if err != nil || view == nil {
		t.Fatalf("read node %s: %v", nodeID, err)
	}
	if view.Name != name {
		t.Fatalf("node %s stored name = %q, want %q", nodeID, view.Name, name)
	}
}
