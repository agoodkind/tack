package integration_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/tack/internal/clock"
)

// searchMixedChangesPlanned is the number of metadata changes and the number of moves that
// the short run requests.
const searchMixedChangesPlanned = 2

type searchMixedChangesOutput struct {
	Result struct {
		Writes struct {
			MetadataChanges int `json:"metadata_changes"`
			Moves           int `json:"moves"`
			Failed          int `json:"failed"`
		} `json:"writes"`
		MetadataNotFound int `json:"metadata_not_found"`
		MovesUnverified  int `json:"moves_unverified"`
		CatchUp          struct {
			Completed bool `json:"completed"`
		} `json:"catch_up"`
	} `json:"result"`
}

// TestSearchMixedMetadataChangesAndMoves runs a short search-mixed workload over HTTP against two
// runtime graphs in the test process with --metadata-writes 2 and --moves 2. It requires the
// command to return without an error after catch-up completes. It requires writes.metadata_changes
// and writes.moves to equal 2. It requires writes.failed, metadata_not_found, and moves_unverified
// to equal 0.
func TestSearchMixedMetadataChangesAndMoves(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	endpoints := startSearchMixedProcesses(t, cfg)
	seed := strconv.FormatInt(clock.Now().UnixNano(), 10)
	planned := strconv.Itoa(searchMixedChangesPlanned)
	output, err := runSearchMixedCommand(t, cfg, "--seed", seed, "--rate", "30", "--write-rate", "60", "--duration", "30s",
		"--concurrency", "4", "--burst-writes", "10", "--catch-up", "5m", "--metadata-writes", planned, "--moves", planned,
		"--endpoints", strings.Join(endpoints, ","), "--commit")
	if err != nil {
		t.Fatalf("qa datagen search-mixed: the workload with metadata changes and moves failed: %v (output=%s)", err, output)
	}
	var envelope searchMixedChangesOutput
	if decodeErr := json.Unmarshal(output, &envelope); decodeErr != nil {
		t.Fatalf("qa datagen search-mixed: the test could not decode the result: %v (output=%s)", decodeErr, output)
	}
	result := envelope.Result
	if result.Writes.MetadataChanges != searchMixedChangesPlanned || result.Writes.Moves != searchMixedChangesPlanned || result.Writes.Failed != 0 {
		t.Fatalf("qa datagen search-mixed: the run must commit %d metadata changes and %d moves without failed writes: %s",
			searchMixedChangesPlanned, searchMixedChangesPlanned, output)
	}
	if result.MetadataNotFound != 0 || result.MovesUnverified != 0 || !result.CatchUp.Completed {
		t.Fatalf("qa datagen search-mixed: search must return every included value and every moved issue only under its new workspace after catch-up: %s", output)
	}
}
