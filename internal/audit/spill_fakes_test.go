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

// memoryOutbox is the spill target for these tests. It keeps what it is given
// so the test can decode it the way the relay would.
type memoryOutbox struct {
	mu      sync.Mutex
	entries []json.RawMessage
	fail    error
	// lastAppendCtxErr records whether the context handed to Append was
	// already done, which is the cancellation-inheritance defect under test.
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

// refuseProduce makes the fake broker answer every produce with an error the
// client does not retry, so a refusal is immediate rather than waiting out the
// produce timeout. The stop function restores normal produces, which is the
// broker coming back.
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
			// Produce v13 keys the response by topic id rather than name, and
			// kgo re-enqueues a partition it cannot find in the reply, so a
			// response that echoes only the name never fails the record.
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

// captureLogs routes the default logger into a buffer for the test's
// duration, so the per-outage logging contract can be counted rather than
// described.
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
