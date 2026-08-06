package datagen

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"goodkind.io/tack/internal/clock"
)

func TestPrepareSoakProjectsUsesOnlyServerProjects(t *testing.T) {
	projectID := deterministicUUID("server-project").String()
	fake := &serverCatalogMCP{
		projects: map[string][]collectionItem{
			"workspace-a": {{Reference: projectID, Name: "Server project"}},
			"workspace-b": nil,
		},
	}
	session := newServerCatalogSession(
		t,
		fake,
		"medium",
		true,
		[]WorkspaceIdentity{
			testSoakWorkspace("workspace-a"),
			testSoakWorkspace("workspace-b"),
		},
	)
	fallbackCount := 0
	session.seedCorpusStarted = func() {
		fallbackCount++
	}

	projects, err := prepareSoakProjects(t.Context(), t.Context(), session)
	if err != nil {
		t.Fatalf("prepareSoakProjects() error = %v", err)
	}
	if fallbackCount != 0 {
		t.Fatalf("fallback seed count = %d, want 0", fallbackCount)
	}
	if len(projects) != 1 || projects[0].RawID != projectID {
		t.Fatalf("projects = %#v, want only server project %q", projects, projectID)
	}
	for _, reference := range fake.projectReferences {
		if reference != projectID {
			t.Fatalf("project reference = %q, want server UUID %q", reference, projectID)
		}
	}
}

func TestSoakSkipsProjectWhoseListCallFails(t *testing.T) {
	var output bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})
	failedProjectID := deterministicUUID("failed-project").String()
	usableProjectID := deterministicUUID("usable-project").String()
	fake := &serverCatalogMCP{
		projects: map[string][]collectionItem{
			"workspace": {
				{Reference: failedProjectID, Name: "Failed project"},
				{Reference: usableProjectID, Name: "Usable project"},
			},
		},
		failedLists: map[string]bool{
			"tack_list_states:" + failedProjectID: true,
		},
	}
	session := newServerCatalogSession(
		t,
		fake,
		"medium",
		false,
		[]WorkspaceIdentity{testSoakWorkspace("workspace")},
	)
	projects, err := prepareSoakProjects(t.Context(), t.Context(), session)
	if err != nil {
		t.Fatalf("prepareSoakProjects() error = %v", err)
	}
	if len(projects) != 1 || projects[0].RawID != usableProjectID {
		t.Fatalf("projects = %#v, want only usable project %q", projects, usableProjectID)
	}
	soak := &Soak{
		driver: session.driver, content: session.content, projects: projects,
		options: SoakOptions{Rate: 1_000_000, MaxOps: 1},
		clock:   clock.Wall{},
	}
	summary, err := soak.Run(t.Context(), t.Context(), nil)
	if err != nil {
		t.Fatalf("Soak.Run() error = %v", err)
	}
	if summary.Operations != 1 || summary.Updated != 1 {
		t.Fatalf("Soak.Run() summary = %#v", summary)
	}
	logs := output.String()
	if !strings.Contains(logs, "qa.datagen.soak.discovery_skipped") ||
		!strings.Contains(logs, failedProjectID) {
		t.Fatalf("logs = %q, want failed project warning", logs)
	}
}

func TestPrepareSoakProjectsFailsAfterSeedLeavesNoProjects(t *testing.T) {
	fake := &serverCatalogMCP{projects: map[string][]collectionItem{"workspace": nil}}
	session := newServerCatalogSession(
		t,
		fake,
		"small",
		true,
		[]WorkspaceIdentity{testSoakWorkspace("workspace")},
	)
	fallbackCount := 0
	session.seedCorpusStarted = func() {
		fallbackCount++
	}

	_, err := prepareSoakProjects(t.Context(), t.Context(), session)
	if err == nil || !strings.Contains(err.Error(), "no usable projects after fallback seed") {
		t.Fatalf("prepareSoakProjects() error = %v", err)
	}
	if fallbackCount != 1 {
		t.Fatalf("fallback seed count = %d, want 1", fallbackCount)
	}
}

func newServerCatalogSession(
	t *testing.T,
	fake *serverCatalogMCP,
	scaleName string,
	dryRun bool,
	workspaces []WorkspaceIdentity,
) *runSession {
	t.Helper()
	const seed int64 = 777
	content, err := NewContent(t.Context(), seed)
	if err != nil {
		t.Fatalf("NewContent() error = %v", err)
	}
	scale, err := ParseScale(scaleName)
	if err != nil {
		t.Fatalf("ParseScale() error = %v", err)
	}
	return &runSession{
		driver: NewDriver(testGraph(fake), false, seed), content: content,
		identities: Identities{Workspaces: workspaces}, scale: scale, seed: seed,
		dryRun: dryRun,
	}
}

func testSoakWorkspace(slug string) WorkspaceIdentity {
	return WorkspaceIdentity{
		Slug: slug,
		Actors: []Actor{{
			UserID: deterministicUUID(slug + "-actor"),
			Token:  slug + "-token",
		}},
	}
}
