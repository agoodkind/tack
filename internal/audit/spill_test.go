package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestSpillKeepsAnEventTheBrokerRefused is TACK-336's producer half. An event
// the broker refuses used to be dropped with a log line; the 2026-07-06 gap
// left eight issue creations in the store with no ledger row that way. Here
// the refused event lands in the outbox, decodes back to the same event with
// its id and time intact, and the caller is told nothing went wrong, because
// nothing did: the event is durable.
func TestSpillKeepsAnEventTheBrokerRefused(t *testing.T) {
	cluster := newFakeCluster(t)
	stop := refuseProduce(cluster)
	t.Cleanup(stop)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}
	event := spillTestEvent()

	if err := recorder.Record(context.Background(), event); err != nil {
		t.Fatalf("Record = %v, want nil: a spilled event is durable and the caller has nothing to act on", err)
	}
	if outbox.count() != 1 {
		t.Fatalf("outbox entries = %d, want the refused event", outbox.count())
	}
	var stored Event
	if err := json.Unmarshal(outbox.entries[0], &stored); err != nil {
		t.Fatalf("the relay could not decode what the spill stored: %v", err)
	}
	if stored.EventID != event.EventID || !stored.OccurredAt.Equal(event.OccurredAt) {
		t.Fatalf("stored id %s at %s, want %s at %s: the relay keys the chain on both",
			stored.EventID, stored.OccurredAt, event.EventID, event.OccurredAt)
	}
}

// TestSpillLogsOnceForAnOutageNotOncePerEvent is TACK-320. Twenty refused
// events during one outage cost one Error line when it began and one Info
// line when it ended, with the count carried on the second, rather than
// twenty Error lines that bury whatever else went wrong that hour.
func TestSpillLogsOnceForAnOutageNotOncePerEvent(t *testing.T) {
	cluster := newFakeCluster(t)
	logs := captureLogs(t)
	stop := refuseProduce(cluster)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}

	const spilled = 20
	for range spilled {
		if err := recorder.Record(context.Background(), spillTestEvent()); err != nil {
			t.Fatalf("Record during outage: %v", err)
		}
	}
	stop()
	if err := recorder.Record(context.Background(), spillTestEvent()); err != nil {
		t.Fatalf("Record after the broker returned: %v", err)
	}

	logged := logs.String()
	if got := strings.Count(logged, "audit.outage_began"); got != 1 {
		t.Fatalf("outage_began lines = %d, want exactly 1 for one outage", got)
	}
	if got := strings.Count(logged, "level=ERROR"); got != 1 {
		t.Fatalf("Error lines = %d, want exactly 1: the outage, not each event\n%s", got, logged)
	}
	if got := strings.Count(logged, "level=WARN"); got != 1 {
		t.Fatalf("Warn lines = %d, want exactly 1: the producer names the refusal once, not per event\n%s", got, logged)
	}
	if !strings.Contains(logged, "kafka.produce.recovered") {
		t.Fatalf("want the producer to log kafka.produce.recovered when the broker returns, got:\n%s", logged)
	}
	if !strings.Contains(logged, "audit.outage_ended") || !strings.Contains(logged, "spilled=20") {
		t.Fatalf("want one outage_ended line carrying spilled=20, got:\n%s", logged)
	}
	if outbox.count() != spilled {
		t.Fatalf("outbox entries = %d, want %d: every refused event must land", outbox.count(), spilled)
	}
}

// TestSpillFailureIsReportedAsLoss pins the one case that is a real loss: the
// broker refused and the outbox could not take the event either. That returns
// an error naming both failures, because there is nowhere left to put the
// event and the caller must know it is gone.
func TestSpillFailureIsReportedAsLoss(t *testing.T) {
	cluster := newFakeCluster(t)
	stop := refuseProduce(cluster)
	t.Cleanup(stop)
	outboxErr := errors.New("foundationdb unavailable")
	outbox := &memoryOutbox{fail: outboxErr}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}

	err := recorder.Record(context.Background(), spillTestEvent())

	if !errors.Is(err, outboxErr) {
		t.Fatalf("err = %v, want the spill failure surfaced: the event is lost", err)
	}
}

// TestSpillIsNotUsedWhileTheBrokerAnswers pins that the spill is a fallback
// and not a second copy: a healthy broker takes every event and the outbox
// stays empty, so the relay never redelivers what already arrived.
func TestSpillIsNotUsedWhileTheBrokerAnswers(t *testing.T) {
	cluster := newFakeCluster(t)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}

	if err := recorder.Record(context.Background(), spillTestEvent()); err != nil {
		t.Fatalf("Record against a healthy broker: %v", err)
	}
	if outbox.count() != 0 {
		t.Fatalf("outbox entries = %d, want 0: a delivered event must not also be spilled", outbox.count())
	}
}

// TestSpillSurvivesTheRequestContextExpiring pins the review finding on the
// first version: the spill ran on the request context, and the primary may
// have failed precisely because that context expired, so the fallback failed
// the same way after the product write had committed. Here the request is
// already cancelled when the primary refuses, and the outbox must still be
// handed a live context.
func TestSpillSurvivesTheRequestContextExpiring(t *testing.T) {
	cluster := newFakeCluster(t)
	stop := refuseProduce(cluster)
	t.Cleanup(stop)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := recorder.Record(requestCtx, spillTestEvent()); err != nil {
		t.Fatalf("Record = %v, want nil: the spill must not inherit the request's cancellation", err)
	}
	if outbox.count() != 1 {
		t.Fatalf("outbox entries = %d, want the refused event", outbox.count())
	}
	if outbox.lastAppendCtxErr != nil {
		t.Fatalf("the outbox was handed a context already done (%v); the spill must run on its own deadline", outbox.lastAppendCtxErr)
	}
}
