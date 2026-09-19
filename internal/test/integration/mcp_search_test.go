package integration

import (
	"strings"
	"testing"
	"time"
)

func TestSearchFindsIssueByTitleWord(t *testing.T) {
	harness := NewMCPHarness(t)
	reference := harness.CreateIssue(t, "Temporal WAL archive rollout")
	arguments := harness.projectArgs()
	arguments.ProjectReference = ""
	arguments.Query = "archive"

	found := waitFor(t, 10*time.Second, func() bool {
		text := harness.Call(t, "tack_search", arguments).Text()
		return strings.Contains(text, reference)
	})

	if !found {
		t.Fatalf("search for %q never returned %s", "archive", reference)
	}
}

func TestSearchReportsUnavailableBackend(t *testing.T) {
	t.Setenv("MEILI_URL", "http://[::1]:1")
	harness := NewMCPHarness(t)
	arguments := harness.projectArgs()
	arguments.ProjectReference = ""
	arguments.Query = "anything"

	text := harness.CallExpectError(t, "tack_search", arguments)

	if !strings.Contains(text, "Search is unavailable") {
		t.Fatalf("unexpected error text:\n%s", text)
	}
}
