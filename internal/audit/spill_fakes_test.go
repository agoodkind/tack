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

// memoryOutbox retains event payloads for decoding as the relay would decode them.
type memoryOutbox struct {
	mu      sync.Mutex
	entries []json.RawMessage
	fail    error
	// lastAppendCtxErr records whether Append received an already-canceled
	// context, the cancellation defect under test.
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

// refuseProduce makes the broker return a nonretryable error for each produce
// instead of waiting for the produce timeout. The returned function restores
// normal broker responses.
func refuseProduce(cluster *kfake.Cluster) (stop func()) {
	var stopped sync.Mutex
	active := true
	cluster.ControlKey(
		kmsg.Produce.Int16(),
		func(req kmsg.Request) (kmsg.Response, error, bool) {
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
				// Produce v13 matches responses by topic ID. Without this ID,
				// kgo retries the missing partition instead of failing the record.
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
		},
	)

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

// captureLogs captures default logger output for outage log count assertions
// and restores the previous logger during cleanup.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(
		slog.New(
			slog.NewTextHandler(
				&buffer,
				&slog.HandlerOptions{
					AddSource:   false,
					Level:       slog.LevelDebug,
					ReplaceAttr: nil,
				},
			),
		),
	)
	t.Cleanup(func() { slog.SetDefault(previous) })

	return &buffer
}
