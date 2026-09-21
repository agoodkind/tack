package integration

import (
	"encoding/json"
	"slices"
	"sort"
	"testing"
)

type outageScenario struct {
	name string
	run  func(*testing.T, *MCPHarness, *outageScenarioState)
}

type outageScenarioState struct {
	ids map[string]string
}

func TestEveryNonSearchToolWorksDuringSearchOutage(t *testing.T) {
	harness := NewMCPHarness(t)
	scenarios := nonSearchOutageScenarios()
	assertScenarioRegistry(t, harness, scenarios)
	state := &outageScenarioState{ids: make(map[string]string)}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			scenario.run(t, harness, state)
		})
	}
}

func assertScenarioRegistry(t *testing.T, harness *MCPHarness, scenarios []outageScenario) {
	t.Helper()
	registered := listRegisteredTools(t, harness)
	nonSearch := make([]string, 0, len(registered)-1)
	searchCount := 0
	for _, name := range registered {
		if name == "tack_search" {
			searchCount++
			continue
		}
		nonSearch = append(nonSearch, name)
	}
	if searchCount != 1 {
		t.Fatalf("tack_search registrations = %d, want 1", searchCount)
	}
	scenarioNames := make([]string, 0, len(scenarios))
	seen := make(map[string]struct{}, len(scenarios))
	for _, scenario := range scenarios {
		if _, duplicate := seen[scenario.name]; duplicate {
			t.Fatalf("duplicate outage scenario for %s", scenario.name)
		}
		seen[scenario.name] = struct{}{}
		scenarioNames = append(scenarioNames, scenario.name)
	}
	sort.Strings(nonSearch)
	sort.Strings(scenarioNames)
	if !slices.Equal(scenarioNames, nonSearch) {
		t.Fatalf("outage scenarios do not equal the non-search registry\nscenarios: %v\nregistry:  %v", scenarioNames, nonSearch)
	}
}

func listRegisteredTools(t *testing.T, harness *MCPHarness) []string {
	t.Helper()
	names := make([]string, 0)
	cursor := ""
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		payload := callRPCRaw(t, harness, "tools/list", params)
		var page struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(payload, &page); err != nil {
			t.Fatalf("decode tools/list result: %v\n%s", err, payload)
		}
		for _, tool := range page.Tools {
			names = append(names, tool.Name)
		}
		if page.NextCursor == "" {
			return names
		}
		cursor = page.NextCursor
	}
}
