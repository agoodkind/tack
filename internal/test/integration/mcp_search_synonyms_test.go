package integration

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"goodkind.io/tack/internal/datagen"
)

// createSynonymSet creates one synonym set in the harness's workspace through
// the generated tack_create_synonym_set tool.
func (h *MCPHarness) createSynonymSet(t *testing.T, name string, terms string) string {
	t.Helper()
	arguments := h.projectArgs()
	arguments.ProjectReference = ""
	arguments.Name = name
	arguments.Properties = datagen.NodeProperties{"terms": json.RawMessage(`"` + terms + `"`)}
	return h.Call(t, "tack_create_synonym_set", arguments).Text()
}

func TestCreateSynonymSetThroughMCP(t *testing.T) {
	harness := NewMCPHarness(t)

	text := harness.createSynonymSet(t, "database terms", "db, database")

	if !strings.Contains(text, "database terms") {
		t.Fatalf("unexpected create output:\n%s", text)
	}
}

func TestSearchUsesOrgSynonyms(t *testing.T) {
	harness := NewMCPHarness(t)
	reference := harness.CreateIssue(t, "Database failover drill")
	harness.createSynonymSet(t, "database terms", "db, database")
	arguments := harness.projectArgs()
	arguments.ProjectReference = ""
	arguments.Query = "db"

	found := waitFor(t, 10*time.Second, func() bool {
		return strings.Contains(harness.Call(t, "tack_search", arguments).Text(), reference)
	})

	if !found {
		t.Fatalf("search for db never returned %s", reference)
	}
}

func TestSynonymsDoNotCrossOrgs(t *testing.T) {
	owner := NewMCPHarness(t)
	other := NewMCPHarness(t)
	reference := other.CreateIssue(t, "Database failover drill")
	owner.createSynonymSet(t, "database terms", "db, database")
	indexed := other.projectArgs()
	indexed.ProjectReference = ""
	indexed.Query = "database"
	if !waitFor(t, 10*time.Second, func() bool {
		return strings.Contains(other.Call(t, "tack_search", indexed).Text(), reference)
	}) {
		t.Fatalf("search for database never returned %s", reference)
	}
	arguments := other.projectArgs()
	arguments.ProjectReference = ""
	arguments.Query = "db"

	text := other.Call(t, "tack_search", arguments).Text()

	if strings.Contains(text, reference) {
		t.Fatalf("another org's synonym set changed this org's results:\n%s", text)
	}
}
