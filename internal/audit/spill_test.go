package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestSpillKeepsAnEventTheBrokerRefused checks the producer behavior from
// TACK-336. The original incident account reports that refused events only
// produced log entries on 2026-07-06, leaving eight issue creations without
// audit rows. The test checks that the outbox stores a refused event, decoding
// preserves its ID and time, and the caller receives nil after storage.
func TestSpillKeepsAnEventTheBrokerRefused(t *testing.T) {
	cluster := newFakeCluster(t)
	stop := refuseProduce(cluster)
	t.Cleanup(stop)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}
	event := spillTestEvent()

	if err := recorder.Record(context.Background(), event); err != nil {
		t.Fatalf(
			"Record = %v, want nil: a spilled event is durable and the caller has nothing to act on",
			err,
		)
	}

	if outbox.count() != 1 {
		t.Fatalf("outbox entries = %d, want the refused event", outbox.count())
	}

	var stored Event
	if err := json.Unmarshal(outbox.entries[0], &stored); err != nil {
		t.Fatalf("the relay could not decode what the spill stored: %v", err)
	}

	if stored.EventID != event.EventID || !stored.OccurredAt.Equal(event.OccurredAt) {
		t.Fatalf(
			"stored id %s at %s, want %s at %s: the relay keys the chain on both",
			stored.EventID,
			stored.OccurredAt,
			event.EventID,
			event.OccurredAt,
		)
	}
}

// TestSpillLogsOnceForAnOutageNotOncePerEvent checks TACK-320. Twenty refused
// events during one outage produce one Error line at the start and one Info
// line at recovery. The recovery line includes the count instead of logging
// an Error for each event.
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
		t.Fatalf(
			"Warn lines = %d, want exactly 1: the producer names the refusal once, not per event\n%s",
			got,
			logged,
		)
	}

	if !strings.Contains(logged, "kafka.produce.recovered") {
		t.Fatalf(
			"want the producer to log kafka.produce.recovered when the broker returns, got:\n%s",
			logged,
		)
	}

	if !strings.Contains(logged, "audit.outage_ended") || !strings.Contains(logged, "spilled=20") {
		t.Fatalf("want one outage_ended line carrying spilled=20, got:\n%s", logged)
	}

	if outbox.count() != spilled {
		t.Fatalf(
			"outbox entries = %d, want %d: every refused event must land",
			outbox.count(),
			spilled,
		)
	}
}

// TestSpillFailureIsReportedAsLoss checks an event that both the broker and
// outbox reject. The recorder returns an error with both failures because
// neither path stored the event.
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

// TestSpillIsNotUsedWhileTheBrokerAnswers checks that a healthy broker records
// the event without an outbox copy. The relay cannot redeliver that event
// from the empty outbox.
func TestSpillIsNotUsedWhileTheBrokerAnswers(t *testing.T) {
	cluster := newFakeCluster(t)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}

	if err := recorder.Record(context.Background(), spillTestEvent()); err != nil {
		t.Fatalf("Record against a healthy broker: %v", err)
	}

	if outbox.count() != 0 {
		t.Fatalf(
			"outbox entries = %d, want 0: a delivered event must not also be spilled",
			outbox.count(),
		)
	}
}

// TestSpillSurvivesTheRequestContextExpiring checks the first version's
// review finding. That version reused request cancellation for the spill.
// A primary failure after request expiration also failed the spill after the
// product write committed. This test cancels the request before primary
// rejection and checks that the outbox receives a live context.
func TestSpillSurvivesTheRequestContextExpiring(t *testing.T) {
	cluster := newFakeCluster(t)
	stop := refuseProduce(cluster)
	t.Cleanup(stop)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}
	requestCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := recorder.Record(requestCtx, spillTestEvent()); err != nil {
		t.Fatalf(
			"Record = %v, want nil: the spill must not inherit the request's cancellation",
			err,
		)
	}

	if outbox.count() != 1 {
		t.Fatalf("outbox entries = %d, want the refused event", outbox.count())
	}

	if outbox.lastAppendCtxErr != nil {
		t.Fatalf(
			"the outbox was handed a context already done (%v); the spill must run on its own deadline",
			outbox.lastAppendCtxErr,
		)
	}
}
