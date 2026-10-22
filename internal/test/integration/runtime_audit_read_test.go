package integration_test

import (
	"cmp"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// raceReadCount sets the number of reads that finalization and recovery attempt concurrently.
const raceReadCount = 64

// TestRuntimeAuditReadFinalizeRecoveryRace checks concurrent finalization and recovery
// in separate operating system processes. Each read must save exactly one terminal event.
// FinalizeRead must return audit.ErrReadOwnerLost when recovery saves the incomplete event first.
func TestRuntimeAuditReadFinalizeRecoveryRace(t *testing.T) {
	for _, backend := range readBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			fixtures := make([]readFixture, raceReadCount)
			eventIDs := make([]uuid.UUID, raceReadCount)
			for position := range fixtures {
				fixtures[position] = newReadFixture(t)
				fixtures[position].begin(t, backend.store)
				eventIDs[position] = fixtures[position].admission.Token.EventID
			}
			backend.expire(t, eventIDs...)
			// The child process recovers buckets in ascending order.
			order := make([]int, raceReadCount)
			for position := range order {
				order[position] = position
			}
			slices.SortFunc(order, func(left, right int) int {
				return cmp.Compare(audit.ReadBucket(eventIDs[right]), audit.ReadBucket(eventIDs[left]))
			})
			child := startReadChild(t, readChildActionRecover, backend.childEnv())
			child.signal(t)
			results := make([]error, raceReadCount)
			for _, position := range order {
				results[position] = backend.store.FinalizeRead(t.Context(), fixtures[position].completion)
			}
			recovered, err := strconv.Atoi(child.result(t))
			if err != nil {
				t.Fatalf("parse the recovered count: %v", err)
			}
			lost := 0
			for position, finalizeErr := range results {
				eventID := eventIDs[position]
				switch {
				case finalizeErr == nil:
					requireTerminal(t, backend, eventID, audit.OutcomeCompleted)
				case errors.Is(finalizeErr, audit.ErrReadOwnerLost):
					lost++
					requireTerminal(t, backend, eventID, audit.OutcomeIncomplete)
				default:
					t.Fatalf("finalize read %s: %v", eventID, finalizeErr)
				}
			}
			if recovered != lost {
				t.Fatalf("recovered reads = %d, want %d to match finalizations that lost ownership", recovered, lost)
			}
			t.Logf("%s: finalized %d reads, recovered %d reads", backend.name, raceReadCount-lost, lost)
		})
	}
}

// TestRuntimeAuditReadUnknownCommit retries finalization in this process and a second
// operating system process after the first commit succeeds. The terminal receipt must
// confirm retries with the same owner and digest without saving another event.
// FinalizeRead must reject a different digest.
func TestRuntimeAuditReadUnknownCommit(t *testing.T) {
	for _, backend := range readBackends(t) {
		t.Run(backend.name, func(t *testing.T) {
			fixture := newReadFixture(t)
			fixture.begin(t, backend.store)
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			if err := backend.store.FinalizeRead(t.Context(), fixture.completion); err != nil {
				t.Fatalf("retry the identical finalization: %v", err)
			}
			if result := finalizeInChild(t, backend, fixture.completion); result != readChildCompletedResult {
				t.Fatalf("child retry of the identical finalization = %q, want %q", result, readChildCompletedResult)
			}
			changed := completeRead(t, fixture.completion.Token, fixture.admission.Template,
				fixture.completion.Targets, "a different payload")
			if err := backend.store.FinalizeRead(t.Context(), changed); !errors.Is(err, audit.ErrReadOwnerLost) {
				t.Fatalf("retry with a different digest = %v, want audit.ErrReadOwnerLost", err)
			}
			if result := finalizeInChild(t, backend, changed); result != readChildOwnerLostResult {
				t.Fatalf("child retry with a different digest = %q, want %q", result, readChildOwnerLostResult)
			}
			requireTerminal(t, backend, fixture.admission.Token.EventID, audit.OutcomeCompleted)
		})
	}
}

// finalizeInChild finalizes completion in a second process and returns its result.
func finalizeInChild(t *testing.T, backend *readBackend, completion audit.ReadCompletion) string {
	t.Helper()
	encoded, err := json.Marshal(newChildCompletion(completion))
	if err != nil {
		t.Fatalf("encode the completion: %v", err)
	}
	path := filepath.Join(t.TempDir(), "completion.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write the completion: %v", err)
	}
	child := startReadChild(t, readChildActionFinalize, append(backend.childEnv(), readChildCompletionFile+"="+path))
	child.signal(t)
	return child.result(t)
}

// childCompletion is the JSON form of audit.ReadCompletion for a child process.
type childCompletion struct {
	EventID    uuid.UUID      `json:"event_id"`
	Owner      uuid.UUID      `json:"owner"`
	Generation uint64         `json:"generation"`
	Event      audit.Event    `json:"event"`
	Targets    []audit.Target `json:"targets"`
	Digest     string         `json:"digest"`
}

func newChildCompletion(completion audit.ReadCompletion) childCompletion {
	token := completion.Token
	return childCompletion{
		EventID: token.EventID, Owner: token.Owner, Generation: token.Generation,
		Event: completion.Event, Targets: completion.Targets, Digest: completion.Digest,
	}
}

func (c childCompletion) readCompletion() audit.ReadCompletion {
	return audit.ReadCompletion{
		Token:   audit.ReadToken{EventID: c.EventID, Owner: c.Owner, Generation: c.Generation, AdmissionDeadline: time.Time{}},
		Event:   c.Event,
		Targets: c.Targets,
		Digest:  c.Digest,
	}
}
