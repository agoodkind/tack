package integration_test

import (
	"encoding/json"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"

	"goodkind.io/tack/internal/audit"
)

const (
	// assemblySmallTargets sets the read target count of the smaller read event.
	assemblySmallTargets = 8_000
	// assemblySmallDeltaBytes sets the size of each old and new value of the
	// smaller change event.
	assemblySmallDeltaBytes = 256 << 10
	// assemblyGrowth multiplies the size of the larger event of each kind.
	assemblyGrowth = 4
	// assemblyDeltaHeapFactor limits the peak heap growth of a change event to
	// the growth of the smaller read event plus this multiple of its old and
	// new value bytes.
	assemblyDeltaHeapFactor = 8
	// assemblyHeapSampleInterval sets the delay between heap samples.
	assemblyHeapSampleInterval = 5 * time.Millisecond
	// assemblyGCPercent limits garbage between collections while the test samples
	// the heap.
	assemblyGCPercent = 10
)

// TestRuntimeAuditAssemblyMemoryIsBounded delivers read events with many read
// targets and change events with large old and new values to the production
// consumer, and samples the heap while the consumer stores and assembles their
// parts. The larger event of each kind has four times the parts of the smaller
// event. The peak heap growth of the larger read event must stay within twice
// the growth of the smaller read event. The ledger stores the complete delta of
// a change event in one row. The peak heap growth of each change event must
// stay within the growth of the smaller read event plus eight times its old and
// new value bytes.
func TestRuntimeAuditAssemblyMemoryIsBounded(t *testing.T) {
	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	producer, err := kgo.NewClient(kgo.SeedBrokers(pipeline.brokers), kgo.DefaultProduceTopic(pipeline.topic))
	if err != nil {
		t.Fatalf("Opening the test producer failed: %v", err)
	}
	t.Cleanup(producer.Close)

	small, smallParts := assemblyPeakHeap(t, pipeline, producer, readAssemblyEvent(t, assemblySmallTargets))
	large, largeParts := assemblyPeakHeap(t, pipeline, producer, readAssemblyEvent(t, assemblySmallTargets*assemblyGrowth))
	t.Logf("Read event peak heap growth: %d bytes for %d parts, %d bytes for %d parts", small, smallParts, large, largeParts)
	if large > 2*small {
		t.Fatalf("The read event peak heap growth increased from %d bytes for %d parts to %d bytes for %d parts; the consumer must not hold the read targets of every part",
			small, smallParts, large, largeParts)
	}

	for _, deltaBytes := range []int{assemblySmallDeltaBytes, assemblySmallDeltaBytes * assemblyGrowth} {
		growth, parts := assemblyPeakHeap(t, pipeline, producer, changeAssemblyEvent(t, deltaBytes))
		limit := small + uint64(assemblyDeltaHeapFactor*2*deltaBytes)
		t.Logf("Change event peak heap growth: %d bytes for %d parts and %d delta bytes", growth, parts, 2*deltaBytes)
		if growth > limit {
			t.Fatalf("The change event peak heap growth of %d bytes for %d delta bytes exceeds %d bytes", growth, 2*deltaBytes, limit)
		}
	}
}

// assemblyEvent is an encoded event and its Kafka records.
type assemblyEvent struct {
	eventID uuid.UUID
	parts   int
	records []*kgo.Record
}

// assemblyPeakHeap produces every record of event and waits for the ledger
// row. It returns the highest heap growth above the collected heap before the
// first record and the part count.
func assemblyPeakHeap(t *testing.T, pipeline partsPipeline, producer *kgo.Client, event assemblyEvent) (uint64, int) {
	t.Helper()
	previous := debug.SetGCPercent(assemblyGCPercent)
	defer debug.SetGCPercent(previous)
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	baseline := stats.HeapAlloc
	peak := sampleHeap(baseline)
	if err := producer.ProduceSync(t.Context(), event.records...).FirstErr(); err != nil {
		t.Fatalf("Producing the records of event %s failed: %v", event.eventID, err)
	}
	pipeline.stack().waitForRow(t, event.eventID)
	return peak() - baseline, event.parts
}

// readAssemblyEvent encodes a completed read event with targetCount node
// targets.
func readAssemblyEvent(t *testing.T, targetCount int) assemblyEvent {
	t.Helper()
	targets := make([]audit.Target, 0, targetCount)
	for range targetCount {
		targets = append(targets, readNodeTarget())
	}
	fixture := newReadFixture(t)
	completion := completeRead(t, fixture.completion.Token, fixture.admission.Template, targets, "runtime audit assembly payload")
	return encodeAssemblyEvent(t, completion.Event, targets)
}

// changeAssemblyEvent returns a node change event with old and new values of
// deltaBytes each.
func changeAssemblyEvent(t *testing.T, deltaBytes int) assemblyEvent {
	t.Helper()
	node := readNodeTarget()
	event := completeEvent(uuid.Must(uuid.NewV7()), userActor(), node, audit.OperationDatabaseChange, audit.PhaseChange, audit.OutcomeCompleted)
	value := func(text string) json.RawMessage {
		return json.RawMessage(`{"description":` + strconv.Quote(strings.Repeat(text, deltaBytes/len(text))) + `}`)
	}
	event.Delta = &audit.Delta{Before: value("old value "), After: value("new value "), Changed: []string{"/description"}}
	return encodeAssemblyEvent(t, event, nil)
}

// encodeAssemblyEvent encodes event with targets into Kafka records.
func encodeAssemblyEvent(t *testing.T, event audit.Event, targets []audit.Target) assemblyEvent {
	t.Helper()
	encoded, err := audit.EncodeEvent(event, targets)
	if err != nil {
		t.Fatalf("Encoding event %s failed: %v", event.EventID, err)
	}
	messages, err := encoded.Messages()
	if err != nil {
		t.Fatalf("Encoding the messages of event %s failed: %v", event.EventID, err)
	}
	records := make([]*kgo.Record, 0, len(messages))
	for _, message := range messages {
		records = append(records, &kgo.Record{Key: messageKey(t, message), Value: message})
	}
	return assemblyEvent{eventID: event.EventID, parts: len(encoded.Parts), records: records}
}

// sampleHeap starts a goroutine that records the highest HeapAlloc value. The
// returned function stops the goroutine and returns that value, at least
// baseline.
func sampleHeap(baseline uint64) func() uint64 {
	stop := make(chan struct{})
	var group sync.WaitGroup
	highest := baseline
	group.Go(func() {
		var stats runtime.MemStats
		ticker := time.NewTicker(assemblyHeapSampleInterval)
		defer ticker.Stop()
		for {
			runtime.ReadMemStats(&stats)
			highest = max(highest, stats.HeapAlloc)
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	})
	return func() uint64 {
		close(stop)
		group.Wait()
		return highest
	}
}
