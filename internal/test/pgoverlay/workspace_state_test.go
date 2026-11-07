//go:build pgoverlay

package pgoverlay_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// roleState is the role data that tofu records in state.
type roleState struct {
	ID          string
	Name        string
	OID         int64
	Version     string
	ValidUntil  string
	AppliedName string
}

func (space *workspace) stateValues(t *testing.T) roleState {
	t.Helper()
	output, code := space.run(t, nil, "show", "-json")
	if code != 0 {
		t.Fatalf("tofu show exited %d", code)
	}
	var document struct {
		Values struct {
			RootModule struct {
				Resources []struct {
					Values struct {
						ID          string `json:"id"`
						Name        string `json:"name"`
						OID         int64  `json:"role_oid"`
						Version     string `json:"password_wo_version"`
						ValidUntil  string `json:"valid_until"`
						AppliedName string `json:"password_wo_applied_name"`
					} `json:"values"`
				} `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("read the state document: %v", err)
	}
	resources := document.Values.RootModule.Resources
	if len(resources) != 1 {
		t.Fatalf("state has %d resources, and the test expects 1", len(resources))
	}
	values := resources[0].Values
	return roleState{
		ID: values.ID, Name: values.Name, OID: values.OID,
		Version: values.Version, ValidUntil: values.ValidUntil, AppliedName: values.AppliedName,
	}
}

// stateResourceCount returns the number of resources that tofu records in state.
func (space *workspace) stateResourceCount(t *testing.T) int {
	t.Helper()
	output, code := space.run(t, nil, "show", "-json")
	if code != 0 {
		t.Fatalf("tofu show exited %d", code)
	}
	var document struct {
		Values struct {
			RootModule struct {
				Resources []json.RawMessage `json:"resources"`
			} `json:"root_module"`
		} `json:"values"`
	}
	if err := json.Unmarshal([]byte(output), &document); err != nil {
		t.Fatalf("read the state document: %v", err)
	}
	return len(document.Values.RootModule.Resources)
}

func (space *workspace) requireNoSecrets(t *testing.T, secrets ...string) {
	t.Helper()
	found := []string{space.transcript.String(), string(space.host.ReadFile(t, space.stateFilePath()))}
	for _, text := range found {
		for _, secret := range secrets {
			if strings.Contains(text, secret) {
				t.Fatalf("a generated credential appears in a command output or the state file")
			}
		}
	}
}
