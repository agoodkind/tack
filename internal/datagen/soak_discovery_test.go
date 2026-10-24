package datagen

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestDiscoverSoakProjectsUsesRawIDsForExistingCorpus(t *testing.T) {
	const seed int64 = 777
	fake := &soakDiscoveryMCP{}
	content, err := NewContent(t.Context(), seed)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	scale, err := ParseScale("small")
	if err != nil {
		t.Fatalf("ParseScale() error = %v", err)
	}
	session := &runSession{
		driver:  NewDriver(testGraph(fake), false, seed),
		content: content,
		identities: Identities{Workspaces: []WorkspaceIdentity{{
			Slug: "qa-777-o01-w01",
			Actors: []Actor{{
				Token: "token",
			}},
		}}},
		scale: scale,
		seed:  seed,
	}

	projects, complete, err := discoverSoakProjects(t.Context(), session)
	if err != nil {
		t.Fatalf("discoverSoakProjects() error = %v", err)
	}
	if !complete || len(projects) != 1 {
		t.Fatalf("discoverSoakProjects() = %d projects, complete %t", len(projects), complete)
	}
	project := projects[0]
	if len(project.Labels) != 2 {
		t.Fatalf("discovered labels = %d, want 2", len(project.Labels))
	}
	assertRawUUID(t, "project", project.RawID)
	assertRawUUID(t, "project reference", project.Reference)
	nodes := append(append([]soakNode{}, project.Labels...), project.States...)
	nodes = append(nodes, project.Epics...)
	nodes = append(nodes, project.Cycles...)
	nodes = append(nodes, project.Modules...)
	for _, node := range nodes {
		assertRawUUID(t, node.Name, node.RawID)
		assertRawUUID(t, node.Name+" reference", node.Reference)
	}
	for _, issue := range project.Issues {
		assertRawUUID(t, issue.Name, issue.RawID)
		assertRawUUID(t, issue.Name+" reference", issue.Reference)
	}
	if len(fake.labelCreateArguments) != 0 {
		t.Fatalf("label creates = %v, want none for listed labels", fake.labelCreateArguments)
	}
	wantGets := []string{
		"qa-777-o01-w01::developer-experience-01-w01-seed-777",
		"qa-777-o01-w01::Soak label op-00000007 seed-777",
	}
	if !slices.Equal(fake.labelGets, wantGets) {
		t.Fatalf("label gets = %q, want %q", fake.labelGets, wantGets)
	}
}

func assertRawUUID(t *testing.T, name, value string) {
	t.Helper()
	if _, err := uuid.Parse(value); err != nil {
		t.Fatalf("%s = %q, want raw UUID", name, value)
	}
}
