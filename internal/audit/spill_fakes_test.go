package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// memoryOutbox stores spill payloads for these tests. Tests decode the stored
// payloads as the relay would.
type memoryOutbox struct {
	mu      sync.Mutex
	entries []json.RawMessage
	fail    error
	// lastAppendCtxErr records whether Append received an expired or canceled
	// context. The tests check that the spill does not inherit cancellation.
	lastAppendCtxErr error
}

func (m *memoryOutbox) Append(ctx context.Context, event json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastAppendCtxErr = ctx.Err()
	if m.fail != nil {
		return m.fail
	}
	m.entries = append(m.entries, append(json.RawMessage(nil), event...))
	return nil
}

func (m *memoryOutbox) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// refuseProduce makes the test broker reject every produce with an error
// that the client does not retry. The refusal avoids waiting for the produce
// timeout. The returned stop function restores normal produces to represent
// broker recovery.
func refuseProduce(cluster *kfake.Cluster) (stop func()) {
	var stopped sync.Mutex
	active := true
	cluster.ControlKey(kmsg.Produce.Int16(), func(req kmsg.Request) (kmsg.Response, error, bool) {
		stopped.Lock()
		defer stopped.Unlock()
		if !active {
			return nil, nil, false
		}
		cluster.KeepControl()
		produceReq, ok := req.(*kmsg.ProduceRequest)
		if !ok {
			return nil, nil, false
		}
		resp := produceReq.ResponseKind().(*kmsg.ProduceResponse)
		for _, topic := range produceReq.Topics {
			respTopic := kmsg.NewProduceResponseTopic()
			respTopic.Topic = topic.Topic
			// Produce v13 identifies response topics by topic ID. kgo re-enqueues a
			// partition that it cannot find in the reply. A reply with only the topic
			// name does not fail the record.
			respTopic.TopicID = topic.TopicID
			for _, partition := range topic.Partitions {
				respPartition := kmsg.NewProduceResponseTopicPartition()
				respPartition.Partition = partition.Partition
				respPartition.ErrorCode = kerr.TopicAuthorizationFailed.Code
				respTopic.Partitions = append(respTopic.Partitions, respPartition)
			}
			resp.Topics = append(resp.Topics, respTopic)
		}
		return resp, nil, true
	})
	return func() {
		stopped.Lock()
		active = false
		stopped.Unlock()
	}
}

func spillTestEvent() Event {
	return Event{
		Verb:       string(VerbNodeCreate),
		EventID:    uuid.Must(uuid.NewV7()),
		Actor:      Actor{Type: ActorUser, ID: uuid.Must(uuid.NewV7())},
		Entity:     Entity{Type: "node", ID: uuid.Must(uuid.NewV7())},
		Context:    EventContext{OrgID: uuid.Must(uuid.NewV7()), Source: SourceMCP},
		Outcome:    OutcomeOK,
		OccurredAt: time.Date(2026, time.September, 1, 3, 0, 0, 0, time.UTC),
	}
}

// captureLogs directs the default logger to a buffer for the test's duration.
// The test counts log entries to check the per-outage logging contract.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}
