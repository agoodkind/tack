package integration

import (
	"strings"
	"testing"
)

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
