//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clock"
	appruntime "goodkind.io/tack/internal/runtime"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireRecorderQuery      = "ashen weir retire recorder"
	retireRecorderClientID   = "tack-retire-recorder-test"
	retireRecorderProduceFor = 15 * time.Second
)

// TestSearchRuntimeAuditRetiredSessionRecorderEvidence runs the production
// search worker with the Kafka recorder and the spill recorder. It pauses
// FoundationDB after the worker saves its close attempt. Cleanup, the failed
// event write, and the failure code save all fail. While FoundationDB remains
// paused, the test checks that audit.events contains the failed row with both
// failure codes. The test resumes FoundationDB and waits for the lease to expire.
// After the worker recovers the attempt, the test checks that audit.events
// contains the incomplete row beside the failed row under one operation.
func TestSearchRuntimeAuditRetiredSessionRecorderEvidence(t *testing.T) {
	setShortSessions(t)
	fixture := newSessionAuditFixture(t)
	fixture.putCorpus(t, retireRecorderQuery, sessionAuditNodes)
	page := fixture.mustSearch(t, fixture.first.Actors[0].Token, retireRecorderQuery, "")
	session := sessionOfCursor(t, fixture.parentEdgeFixture, page.Cursor)
	source := servingIndex(t, fixture.parentEdgeFixture)
	beginReplacement(t, fixture.parentEdgeFixture)
	rebuilds := fixture.stores.SearchRebuilds(clock.Wall{})
	rebuild, found, err := rebuilds.CurrentRebuild(t.Context())
	if err != nil || !found {
		t.Fatalf("read the index replacement: found %t, %v", found, err)
	}
	kafka, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: audit.SplitBrokers(testenv.Kafka(t)), Topic: fixture.ledger.stack.topic,
		ClientID: retireRecorderClientID, ProduceTimeout: retireRecorderProduceFor,
	})
	if err != nil {
		t.Fatalf("open the Kafka recorder: %v", err)
	}
	t.Cleanup(func() { _ = kafka.Close() })
	recorder := audit.CanonicalRecorder{Inner: &audit.SpillRecorder{Primary: kafka, Spill: fixture.stores.OpsOutbox}}
	fixture.worker, _ = newParentEdgeWorkerWith(t, fixture.cfg, fixture.stores, rebuilds.WithFailureRecorder(recorder))

	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	paused := false
	hooks := installRetireLogHooks(t, session.ID.String(), map[string]func(){
		retireAttemptSavedMessage: func() {
			setContainerPaused(t, container, true)
			paused = true
		},
	})
	for !paused {
		if _, err := fixture.worker.RunSlice(t.Context()); err != nil {
			t.Logf("the search worker slice failed: %v", err)
		}
	}
	hooks.requireFinished(t)
	rows := fixture.waitForRows(t, fixture.first.OrgID, func(current []audit.Row) bool {
		return len(failedCloseRows(current, session.ID)) == 1
	})
	failed := onlyRow(t, failedCloseRows(rows, session.ID), "the recorded cleanup failure")
	workerID := cli.ServiceActorID(appruntime.SearchWorkerService)
	requireRetirementRow(t, failed, workerID, rebuild, source)
	requireRecordedCodes(t, failed.EventID, failed.Extra)

	setContainerPaused(t, container, false)
	paused = false
	runReplacementThroughFailures(t, fixture.parentEdgeFixture)
	rows = fixture.waitForRows(t, fixture.first.OrgID, func(current []audit.Row) bool {
		return len(lostRetireRows(current, session.ID)) == 1
	})
	lost := onlyRow(t, lostRetireRows(rows, session.ID), "the incomplete cleanup result")
	requireRetirementRow(t, lost, workerID, rebuild, source)
	if lost.EventID == failed.EventID || lost.Context.OperationID != failed.Context.OperationID {
		t.Fatalf("incomplete row %s under operation %s and failed row %s under operation %s, want separate rows of one operation",
			lost.EventID, lost.Context.OperationID, failed.EventID, failed.Context.OperationID)
	}
}

func requireRecordedCodes(t *testing.T, eventID uuid.UUID, extra json.RawMessage) {
	t.Helper()
	var evidence retireFailureEvidence
	if err := json.Unmarshal(extra, &evidence); err != nil || evidence.Cleanup != transactionTimedOutCode || evidence.Recording != transactionTimedOutCode {
		t.Fatalf("row %s evidence = %s, %v, want cleanup and recording code %q", eventID, extra, err, transactionTimedOutCode)
	}
}
