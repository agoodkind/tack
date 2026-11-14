package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestSpillStoresBrokerRefusedEvent checks durable outbox storage with the
// original event ID and time and a successful caller result (TACK-336).
func TestSpillStoresBrokerRefusedEvent(t *testing.T) {
	cluster := newFakeCluster(t)
	stop := refuseProduce(cluster)
	t.Cleanup(stop)
	outbox := &memoryOutbox{}
	recorder := &SpillRecorder{Primary: newKafkaRecorderForTest(t, cluster), Spill: outbox}
	event := spillTestEvent()

	if err := recorder.Record(context.Background(), event); err != nil {
		t.Fatalf(
			"Record = %v, want nil after durable outbox storage",
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

// TestSpillLogsOnceForAnOutageNotOncePerEvent checks one outage start and
// recovery log with the total spill count across twenty refusals (TACK-320).
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
			"Warn lines = %d, want exactly 1: the producer reports the refusal once, not per event\n%s",
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
		t.Fatalf("want one outage_ended line including spilled=20, got:\n%s", logged)
	}

	if outbox.count() != spilled {
		t.Fatalf(
			"outbox entries = %d, want %d stored refused events",
			outbox.count(),
			spilled,
		)
	}
}

// TestSpillFailureIsReportedAsLoss checks that failure of both the broker and
// outbox returns the outbox error to the caller.
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

// TestSpillSkipsOutboxAfterBrokerSuccess checks that successful produce does
// not create an outbox copy for duplicate relay delivery.
func TestSpillSkipsOutboxAfterBrokerSuccess(t *testing.T) {
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

// TestSpillAppendsAfterRequestCancellation checks that the outbox receives a
// live context with its own deadline after the canceled primary attempt.
func TestSpillAppendsAfterRequestCancellation(t *testing.T) {
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
