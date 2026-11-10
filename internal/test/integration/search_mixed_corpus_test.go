package integration_test

import (
	"strings"
	"testing"
)

type searchMixedCorpusIndex struct {
	Threshold string `json:"threshold"`
	Waits     []struct {
		Group     string  `json:"group"`
		Tenant    int     `json:"tenant"`
		Nodes     int     `json:"nodes"`
		Checks    int     `json:"checks"`
		ElapsedMs float64 `json:"elapsed_ms"`
	} `json:"waits"`
}

// requireSearchMixedCorpusIndex requires one finished wait for each corpus group in wait order,
// with a positive elapsed time that does not decrease between groups, and the statement that no
// threshold exists.
func requireSearchMixedCorpusIndex(t *testing.T, output []byte, index searchMixedCorpusIndex) {
	t.Helper()
	if !strings.HasPrefix(index.Threshold, "none:") || len(index.Waits) != 3 {
		t.Fatalf("qa datagen search-mixed: corpus_index must state that no threshold exists and list three corpus waits: %s", output)
	}
	groups := []string{"read", "foreign_read", "victims"}
	tenants := []int{0, 1, 0}
	previous := 0.0
	for position, wait := range index.Waits {
		if wait.Group != groups[position] || wait.Tenant != tenants[position] || wait.Nodes <= 0 || wait.Checks < 1 {
			t.Fatalf("qa datagen search-mixed: corpus wait %d must report group %s, tenant %d, issues, and checks: %s",
				position, groups[position], tenants[position], output)
		}
		if wait.ElapsedMs <= 0 || wait.ElapsedMs < previous {
			t.Fatalf("qa datagen search-mixed: corpus wait %d must report a positive elapsed time that is not less than the previous wait: %s", position, output)
		}
		previous = wait.ElapsedMs
	}
	if index.Waits[0].Nodes != 140 || index.Waits[1].Nodes != 10 {
		t.Fatalf("qa datagen search-mixed: the read corpus waits must cover 140 and 10 issues: %s", output)
	}
}

// TestSearchMixedCorpusWaitStall runs the search-mixed command over HTTP against two runtime
// graphs that do not run search workers. Search does not return any corpus issue, and the first
// corpus wait must fail after the stall period with the omitted count and the first omitted issue.
func TestSearchMixedCorpusWaitStall(t *testing.T) {
	cfg := newCohortEngines(t)
	_, first := serveSearchMixedProcess(t, cfg)
	_, second := serveSearchMixedProcess(t, cfg)
	output, err := runSearchMixedCommand(t, cfg, "--rate", "60", "--write-rate", "60", "--duration", "40s",
		"--concurrency", "8", "--burst-writes", "60", "--endpoints", first+","+second, "--commit")
	if err == nil || len(output) != 0 {
		t.Fatalf("qa datagen search-mixed: the corpus wait must fail before the command writes a result (error=%v; output=%s)", err, output)
	}
	message := err.Error()
	if !strings.Contains(message, "search omitted 140 of 140 corpus nodes") || !strings.Contains(message, "the first missing node is ") {
		t.Fatalf("qa datagen search-mixed: the stall error must report the omitted count and the first missing node: %v", err)
	}
}
