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
	// assemblyReadGrowth multiplies the target count of the larger read event.
	assemblyReadGrowth = 16
	// assemblySmallDeltaBytes sets the size of each old and new value of the
	// smaller change event.
	assemblySmallDeltaBytes = 256 << 10
	// assemblyDeltaGrowth multiplies the delta size of the larger change event.
	assemblyDeltaGrowth = 4
	// assemblyDeltaHeapFactor limits the peak heap growth of a change event to
	// the growth of the smaller read event plus this multiple of its old and
	// new value bytes.
	assemblyDeltaHeapFactor = 8
	// assemblyHeapRuns sets how many events of each size the test measures.
	// The test keeps the smallest peak of the runs.
	assemblyHeapRuns = 3
	// assemblyHeapSampleInterval sets the delay between heap samples.
	assemblyHeapSampleInterval = 5 * time.Millisecond
	// assemblyGCPercent limits garbage between collections while the test samples
	// the heap.
	assemblyGCPercent = 10
)

// TestRuntimeAuditAssemblyMemoryIsBounded stores every part of an event in the
// production consumer, then samples the heap while the consumer receives the
// manifest and assembles the event. Read events have many read targets, and
// change events have large old and new values. Each size runs
// assemblyHeapRuns times, and the test keeps the smallest peak heap growth.
// The larger read event has sixteen times the parts of the smaller read event.
// Its peak heap growth must exceed the growth of the smaller read event by less
// than the payload bytes of the additional parts. The ledger stores the
// complete delta of a change event in one row. The peak heap growth of each
// change event must stay within the growth of the smaller read event plus
// eight times its old and new value bytes.
func TestRuntimeAuditAssemblyMemoryIsBounded(t *testing.T) {
	pipeline := newPartsPipeline(t)
	pipeline.startConsumer(t)
	producer, err := kgo.NewClient(kgo.SeedBrokers(pipeline.brokers), kgo.DefaultProduceTopic(pipeline.topic))
	if err != nil {
		t.Fatalf("Opening the test producer failed: %v", err)
	}
	t.Cleanup(producer.Close)

	small, smallParts := assemblyPeakHeap(t, pipeline, producer, func() assemblyEvent { return readAssemblyEvent(t, assemblySmallTargets) })
	large, largeParts := assemblyPeakHeap(t, pipeline, producer, func() assemblyEvent {
		return readAssemblyEvent(t, assemblySmallTargets*assemblyReadGrowth)
	})
	partBytes := int64(largeParts-smallParts) * audit.EventPartMaxBytes
	t.Logf("Read event peak heap growth: %d bytes for %d parts, %d bytes for %d parts", small, smallParts, large, largeParts)
	if int64(large)-int64(small) >= partBytes {
		t.Fatalf("The read event peak heap growth increased from %d bytes for %d parts to %d bytes for %d parts, at least the %d payload bytes of the additional parts; the consumer must not hold the read targets of every part",
			small, smallParts, large, largeParts, partBytes)
	}

	for _, deltaBytes := range []int{assemblySmallDeltaBytes, assemblySmallDeltaBytes * assemblyDeltaGrowth} {
		growth, parts := assemblyPeakHeap(t, pipeline, producer, func() assemblyEvent { return changeAssemblyEvent(t, deltaBytes) })
		limit := small + uint64(assemblyDeltaHeapFactor*2*deltaBytes)
		t.Logf("Change event peak heap growth: %d bytes for %d parts and %d delta bytes", growth, parts, 2*deltaBytes)
		if growth > limit {
			t.Fatalf("The change event peak heap growth of %d bytes for %d delta bytes exceeds %d bytes", growth, 2*deltaBytes, limit)
		}
	}
}

// assemblyEvent is an encoded event, its part records, and its manifest
// record.
type assemblyEvent struct {
	eventID  uuid.UUID
	parts    []*kgo.Record
	manifest *kgo.Record
}

// assemblyPeakHeap builds and delivers assemblyHeapRuns events. For each event,
// it produces the part records and waits until the consumer stores every part.
// It then collects the heap, produces the manifest record, and samples the heap
// until the ledger row exists. It returns the smallest peak heap growth of the
// runs and the part count.
func assemblyPeakHeap(t *testing.T, pipeline partsPipeline, producer *kgo.Client, build func() assemblyEvent) (uint64, int) {
	t.Helper()
	var smallest uint64
	parts := 0
	for run := range assemblyHeapRuns {
		event := build()
		parts = len(event.parts)
		if err := producer.ProduceSync(t.Context(), event.parts...).FirstErr(); err != nil {
			t.Fatalf("Producing the parts of event %s failed: %v", event.eventID, err)
		}
		waitFor(t, "storage of every part", func() bool {
			return pipeline.count(t, assemblyPartRowsQuery, event.eventID) == int64(parts)
		})
		previous := debug.SetGCPercent(assemblyGCPercent)
		runtime.GC()
		var stats runtime.MemStats
		runtime.ReadMemStats(&stats)
		baseline := stats.HeapAlloc
		peak := sampleHeap(baseline)
		if err := producer.ProduceSync(t.Context(), event.manifest).FirstErr(); err != nil {
			t.Fatalf("Producing the manifest of event %s failed: %v", event.eventID, err)
		}
		pipeline.stack().waitForRow(t, event.eventID)
		growth := peak() - baseline
		debug.SetGCPercent(previous)
		if run == 0 || growth < smallest {
			smallest = growth
		}
	}
	return smallest, parts
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

// encodeAssemblyEvent encodes event with targets into Kafka records. The last
// message is the manifest.
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
	return assemblyEvent{eventID: event.EventID, parts: records[:len(records)-1], manifest: records[len(records)-1]}
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
