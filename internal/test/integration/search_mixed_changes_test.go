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
		MetadataNotFound        int `json:"metadata_not_found"`
		MovesUnverified         int `json:"moves_unverified"`
		MetadataAbsentBefore    int `json:"metadata_absent_before"`
		MetadataPresentBefore   int `json:"metadata_present_before"`
		MetadataUncheckedBefore int `json:"metadata_unchecked_before"`
		CatchUp                 struct {
			Completed      bool    `json:"completed"`
			ElapsedMs      float64 `json:"elapsed_ms"`
			DrainedAfterMs float64 `json:"drained_after_ms"`
		} `json:"catch_up"`
	} `json:"result"`
}

// TestSearchMixedMetadataChangesAndMoves runs a short search-mixed workload over HTTP against two
// runtime graphs in the test process with --metadata-writes 2 and --moves 2. It requires the
// command to return without an error and catch_up.completed to be true. It requires
// writes.metadata_changes and writes.moves to equal 2. It requires writes.failed,
// metadata_not_found, and moves_unverified to equal 0. It requires metadata_absent_before to
// equal 2 and metadata_present_before and metadata_unchecked_before to equal 0. It requires
// catch_up.drained_after_ms to be positive and not greater than catch_up.elapsed_ms.
func TestSearchMixedMetadataChangesAndMoves(t *testing.T) {
	cfg := newCohortEngines(t)
	t.Setenv("OPENSEARCH_WORKER_CONCURRENCY", "1")
	t.Setenv("OPENSEARCH_WORKER_IDLE_INTERVAL", "1s")
	t.Setenv("OPENSEARCH_SESSION_IDLE_TIMEOUT", "30s")
	endpoints := startSearchMixedProcesses(t, cfg)
	seed := strconv.FormatInt(clock.Now().UnixNano(), 10)
	planned := strconv.Itoa(searchMixedChangesPlanned)
	output, err := runSearchMixedCommand(t, cfg, "--seed", seed, "--rate", "30", "--write-rate", "60", "--duration", "30s",
		"--concurrency", "4", "--burst-writes", "10", "--metadata-writes", planned, "--moves", planned,
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
	if result.MetadataAbsentBefore != searchMixedChangesPlanned || result.MetadataPresentBefore != 0 || result.MetadataUncheckedBefore != 0 {
		t.Fatalf("qa datagen search-mixed: metadata_absent_before must equal 2, and metadata_present_before and metadata_unchecked_before must equal 0: %s", output)
	}
	catchUp := result.CatchUp
	if catchUp.DrainedAfterMs <= 0 || catchUp.DrainedAfterMs > catchUp.ElapsedMs {
		t.Fatalf("qa datagen search-mixed: catch_up.drained_after_ms must be positive and must not exceed catch_up.elapsed_ms: %s", output)
	}
}
