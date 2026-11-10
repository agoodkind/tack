package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/tack/internal/test/integration"
)

const (
	// sequenceIssueCreates is the number of issues the test creates at the
	// same time, split evenly between the two runtime graphs.
	sequenceIssueCreates = 48
	// sequenceChangeReadTimeout limits the read of the counter change events
	// from the FoundationDB outbox.
	sequenceChangeReadTimeout = 30 * time.Second
)

// sequenceValue is the JSON value of one counter in a change event.
type sequenceValue struct {
	Value int64 `json:"value"`
}

// TestMCPConcurrentIssueCreatesInTwoProcessesNumberEachIssueOnce creates
// issues in one project at the same time through two runtime graphs that use
// the same FoundationDB cluster. Every create must succeed, the issue numbers
// must be exactly 1 through N, and the outbox must contain one change event
// for each counter change with an old value one less than the new value.
func TestMCPConcurrentIssueCreatesInTwoProcessesNumberEachIssueOnce(t *testing.T) {
	first := integration.NewMCPHarness(t)
	processes := []*integration.MCPHarness{first, first.SecondProcess(t)}
	references := createIssuesConcurrently(t, processes, sequenceIssueCreates)
	counterKey, numbers := issueSequenceNumbers(t, references)
	requireOneThroughN(t, "issue numbers", numbers)
	requireSequenceChanges(t, first, counterKey, len(references))
}

func createIssuesConcurrently(t *testing.T, processes []*integration.MCPHarness, count int) []string {
	t.Helper()
	references := make([]string, count)
	failures := make([]error, count)
	var group sync.WaitGroup
	for index := range count {
		group.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					failures[index] = fmt.Errorf("create issue %d panicked: %v", index, recovered)
				}
			}()
			process := processes[index%len(processes)]
			references[index], failures[index] = process.TryCreateIssue(t.Context(), "Concurrent issue "+strconv.Itoa(index))
		})
	}
	group.Wait()
	if err := errors.Join(failures...); err != nil {
		t.Fatalf("every concurrent issue create must succeed: %v", err)
	}
	return references
}

// issueSequenceNumbers splits each reference into the counter key, which
// ends with the last hyphen, and the number after it. Every reference must
// use the same counter key.
func issueSequenceNumbers(t *testing.T, references []string) (string, []int64) {
	t.Helper()
	counterKey := ""
	numbers := make([]int64, 0, len(references))
	for _, reference := range references {
		separator := strings.LastIndex(reference, "-")
		if separator < 0 {
			t.Fatalf("issue reference %q does not contain a hyphen", reference)
		}
		number, err := strconv.ParseInt(reference[separator+1:], 10, 64)
		if err != nil {
			t.Fatalf("issue reference %q does not end with a number: %v", reference, err)
		}
		prefix := reference[:separator+1]
		if counterKey != "" && prefix != counterKey {
			t.Fatalf("issue reference %q uses counter key %q; other issues use %q", reference, prefix, counterKey)
		}
		counterKey = prefix
		numbers = append(numbers, number)
	}
	return counterKey, numbers
}

// requireOneThroughN requires values to contain each number from 1 through
// len(values) exactly once.
func requireOneThroughN(t *testing.T, label string, values []int64) {
	t.Helper()
	sorted := slices.Sorted(slices.Values(values))
	for position, value := range sorted {
		if value != int64(position+1) {
			t.Fatalf("%s must be exactly 1 through %d without a gap or a duplicate; sorted values are %v", label, len(sorted), sorted)
		}
	}
}

// requireSequenceChanges requires one change event for each of count counter
// changes. Each event must record a new value one more than the old value,
// and an absent old value counts as zero.
func requireSequenceChanges(t *testing.T, harness *integration.MCPHarness, counterKey string, count int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), sequenceChangeReadTimeout)
	defer cancel()
	events, err := harness.SequenceChanges(ctx, counterKey)
	if err != nil {
		t.Fatalf("read the change events of sequence counter %q: %v", counterKey, err)
	}
	if len(events) != count {
		t.Fatalf("sequence counter %q has %d change events; want one for each of the %d creates", counterKey, len(events), count)
	}
	newValues := make([]int64, 0, len(events))
	for _, event := range events {
		if event.Delta == nil {
			t.Fatalf("change event %s of sequence counter %q does not record old and new values", event.EventID, counterKey)
		}
		oldValue := int64(0)
		if len(event.Delta.Before) > 0 {
			oldValue = decodeSequenceValue(t, event.Delta.Before)
		}
		newValue := decodeSequenceValue(t, event.Delta.After)
		if newValue != oldValue+1 {
			t.Fatalf("change event %s of sequence counter %q changes %d to %d; want a step of one", event.EventID, counterKey, oldValue, newValue)
		}
		newValues = append(newValues, newValue)
	}
	requireOneThroughN(t, "new counter values in the change events", newValues)
}

func decodeSequenceValue(t *testing.T, raw json.RawMessage) int64 {
	t.Helper()
	var decoded sequenceValue
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode the counter value %s: %v", raw, err)
	}
	return decoded.Value
}
