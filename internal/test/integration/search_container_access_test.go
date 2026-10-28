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
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
)

const (
	// containerAccessIssues sets the number of issues the test creates under the project.
	containerAccessIssues = 20
	// containerAccessName starts each issue name, which ends with a number of three digits.
	containerAccessName = "Cobalt lantern issue"
	// containerAccessQuery matches every issue and does not match the project.
	containerAccessQuery = "cobalt lantern"
	// containerAccessSlices limits worker slices for each full drain.
	containerAccessSlices = 4000
)

// TestSearchContainerFirstAccessKeepsUnchangedPages uses real FoundationDB and OpenSearch.
// The public MCP tools tack_create_project and tack_create_issue create a project and 20 issues.
// The test runs only content work, records each issue's pages, then runs access and remaining work.
// It requires tack_search to return each issue once.
// It deletes one issue with tack_delete_issue, which schedules access work for the project.
// The project's first access slice must schedule access work for every remaining issue.
// It requires one access slice per remaining issue and logs the counts in continuous integration.
// It requires each remaining issue's pages to retain their document IDs, access keys,
// and search generation from the content work.
// It requires tack_search to return the earlier results except the deleted issue, each once.
func TestSearchContainerFirstAccessKeepsUnchangedPages(t *testing.T) {
	fixture := integration.NewQueryFixture(t)
	integration.DrainSearchWork(t, fixture.Worker, containerAccessSlices)
	harness := fixture.Harness
	projectID := createContainerProject(t, harness)
	issues := make([]uuid.UUID, 0, containerAccessIssues)
	for number := range containerAccessIssues {
		issues = append(issues, createContainerIssue(t, harness, number))
	}
	integration.DrainClass(t, fixture, searchdomain.WorkClassLive)
	indexed := containerPages(t, fixture, issues)
	created := integration.DrainClass(t, fixture, searchdomain.WorkClassAccess)
	integration.DrainSearchWork(t, fixture.Worker, containerAccessSlices)
	before := integration.CallEverySearchPage(t, containerAccessQuery, harness)
	requireIssuesFoundOnce(t, before, issues)

	deleted, kept := issues[0], issues[1:]
	arguments := containerArguments(harness)
	arguments.NodeID = deleted.String()
	harness.Call(t, "tack_delete_issue", arguments)
	fanout := integration.DrainClass(t, fixture, searchdomain.WorkClassAccess)
	integration.DrainSearchWork(t, fixture.Worker, containerAccessSlices)

	logContainerAccess(t, projectID, kept, created, fanout)
	for _, issueID := range kept {
		if fanout[issueID] != 1 {
			t.Errorf("issue %s used %d access slices after the delete, want 1", issueID, fanout[issueID])
		}
	}
	if fanout[projectID] == 0 {
		t.Errorf("project %s used no access slice after the delete", projectID)
	}
	requireContainerPagesKept(t, indexed, containerPages(t, fixture, kept))
	remaining := slices.DeleteFunc(slices.Clone(before), func(nodeID uuid.UUID) bool { return nodeID == deleted })
	integration.RequireExactlyOnce(t, integration.CallEverySearchPage(t, containerAccessQuery, harness), remaining)
}

// requireIssuesFoundOnce fails the test unless found contains each ID in issues exactly once.
// It allows other IDs in found.
func requireIssuesFoundOnce(t *testing.T, found, issues []uuid.UUID) {
	t.Helper()
	occurrences := make(map[uuid.UUID]int, len(found))
	for _, nodeID := range found {
		occurrences[nodeID]++
	}
	for _, issueID := range issues {
		if occurrences[issueID] != 1 {
			t.Fatalf("search for %q returned issue %s %d times, want once", containerAccessQuery, issueID, occurrences[issueID])
		}
	}
}

// containerArguments returns MCP tool arguments for the harness workspace and project.
// It leaves every other field empty.
func containerArguments(harness *integration.MCPHarness) datagen.ToolArguments {
	return datagen.ToolArguments{
		WorkspaceReference: harness.Workspace, ProjectReference: harness.Project, IssueReference: "",
		Name: "", Properties: nil, NodeID: "", Query: "", NodeType: "", Direction: "", SourceID: "",
		RelationType: "", TargetID: "", Limit: 0, Cursor: "",
	}
}

// createContainerProject creates a project with a unique identifier through tack_create_project.
// It records the identifier as the harness project and returns the project ID.
func createContainerProject(t *testing.T, harness *integration.MCPHarness) uuid.UUID {
	t.Helper()
	identifier := "CAF" + strconv.FormatInt(clock.Now().UnixNano()%1_000_000, 10)
	arguments := containerArguments(harness)
	arguments.ProjectReference, arguments.Name = "", "Container access project"
	arguments.Properties = datagen.NodeProperties{"identifier": json.RawMessage(strconv.Quote(identifier))}
	created := harness.Call(t, "tack_create_project", arguments)
	projectID, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("parse created project id %q: %v", created.RawID(), err)
	}
	harness.Project = identifier
	return projectID
}

// createContainerIssue creates an issue in the harness project through tack_create_issue.
// It combines containerAccessName and number for the issue name and returns the issue ID.
func createContainerIssue(t *testing.T, harness *integration.MCPHarness, number int) uuid.UUID {
	t.Helper()
	arguments := containerArguments(harness)
	arguments.Name = fmt.Sprintf("%s %03d", containerAccessName, number)
	created := harness.Call(t, "tack_create_issue", arguments)
	issueID, err := uuid.Parse(created.RawID())
	if err != nil {
		t.Fatalf("parse created issue id %q: %v", created.RawID(), err)
	}
	return issueID
}

// containerPages returns active page documents for each node from the serving index.
// It fails the test if a node lacks pages.
func containerPages(t *testing.T, fixture integration.QueryFixture, nodeIDs []uuid.UUID) map[uuid.UUID][]integration.SearchPageSource {
	t.Helper()
	pages := make(map[uuid.UUID][]integration.SearchPageSource, len(nodeIDs))
	for _, nodeID := range nodeIDs {
		pages[nodeID] = integration.SearchNodePages(t, fixture, nodeID)
		if len(pages[nodeID]) == 0 {
			t.Fatalf("node %s has no indexed page", nodeID)
		}
	}
	return pages
}

// requireContainerPagesKept fails the test unless every node in current has the same page documents
// as indexed, with the same access keys and search generation.
func requireContainerPagesKept(t *testing.T, indexed, current map[uuid.UUID][]integration.SearchPageSource) {
	t.Helper()
	for nodeID, pages := range current {
		before := indexed[nodeID]
		if len(pages) != len(before) {
			t.Fatalf("node %s has %d pages, want the %d pages that content work wrote", nodeID, len(pages), len(before))
		}
		for position, page := range pages {
			previous := before[position]
			sameKeys := slices.Equal(page.Access.Keys, previous.Access.Keys)
			if page.ID != previous.ID || !sameKeys || page.SearchGeneration != previous.SearchGeneration {
				t.Errorf("page %s of node %s has keys %v at generation %s, want keys %v at generation %s",
					page.ID, nodeID, page.Access.Keys, page.SearchGeneration, previous.Access.Keys, previous.SearchGeneration)
			}
		}
	}
}

// logContainerAccess logs access slice counts for the project and remaining issues.
// It uses created for counts before deletion and fanout for counts after deletion.
func logContainerAccess(t *testing.T, projectID uuid.UUID, kept []uuid.UUID, created, fanout map[uuid.UUID]int) {
	t.Helper()
	issueSlices := 0
	for _, issueID := range kept {
		issueSlices += fanout[issueID]
	}
	t.Logf("access slices before the delete: %d slices for %d nodes", sumSlices(created), len(created))
	t.Logf("access slices after the delete: %d slices for %d nodes; project %d slices; %d kept issues %d slices",
		sumSlices(fanout), len(fanout), fanout[projectID], len(kept), issueSlices)
}

// sumSlices returns the total of the slice counts.
func sumSlices(counts map[uuid.UUID]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}
