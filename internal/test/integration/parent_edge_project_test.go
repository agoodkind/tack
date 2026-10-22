package integration_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/test/integration"
)

// TestSearchParentEdgeProjectUnderWorkspace creates a project through
// tack_create_project and runs all claimable search work. It requires the
// project and the workspace entry point to have equal, nonempty index access
// keys and equal summary access keys. Search under the workspace must return
// the project within parentEdgeDeadline, and ops search verify must not list
// the project as excluded.
func TestSearchParentEdgeProjectUnderWorkspace(t *testing.T) {
	fixture := newParentEdgeFixture(t)
	name := "garnet project under workspace"
	identifier := "PEP" + strconv.FormatInt(clock.Now().UnixNano()%1_000_000, 10)
	created, err := fixture.driver.Call(integration.WithTestOperation(t, t.Context()), fixture.first.Actors[0].Token, "tack_create_project", datagen.ToolArguments{
		WorkspaceReference: fixture.first.Slug, Name: name,
		Properties: datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))},
	})
	if err != nil {
		t.Fatalf("create project through tack_create_project: %v", err)
	}
	project, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("parse project ID %q: %v", created.RawID(), err)
	}
	fixture.drain(t)

	access, err := fixture.index(t, project)
	if err != nil {
		t.Fatalf("index access of project %s: %v", project, err)
	}
	entryAccess, err := fixture.index(t, fixture.firstEntry)
	if err != nil {
		t.Fatalf("index access of entry point %s: %v", fixture.firstEntry, err)
	}
	if len(access.Keys) == 0 || !slices.Equal(access.Keys, entryAccess.Keys) {
		t.Fatalf("project %s has index keys %v, want the entry point keys %v", project, access.Keys, entryAccess.Keys)
	}
	keys := fixture.summaryKeys(t, project, fixture.firstEntry)
	if len(keys[project]) == 0 || !slices.Equal(keys[project], keys[fixture.firstEntry]) {
		t.Fatalf("project %s has summary keys %v, want the entry point keys %v", project, keys[project], keys[fixture.firstEntry])
	}
	fixture.eventually(t, func() string {
		if found := fixture.search(t, fixture.first, name); !slices.Contains(found, project) {
			return fmt.Sprintf("search under the first entry point returned %v, want project %s", found, project)
		}
		return ""
	})
	if slices.Contains(fixture.excluded(t), project) {
		t.Fatalf("ops search verify lists project %s as excluded", project)
	}
}
