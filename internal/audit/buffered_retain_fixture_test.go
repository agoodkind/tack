package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"goodkind.io/tack/internal/audit"
)

const retainTopic = "audit.events.v1"

// failingOutbox stores events once its failure is cleared.
//
//testdouble:external fakes the FoundationDB operator outbox service.
type failingOutbox struct {
	mu      sync.Mutex
	fail    error
	entries []json.RawMessage
}

func (o *failingOutbox) Append(_ context.Context, event json.RawMessage) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.fail != nil {
		return o.fail
	}
	o.entries = append(o.entries, append(json.RawMessage(nil), event...))
	return nil
}

func (o *failingOutbox) setFail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.fail = err
}

// refuseProduces makes the fake broker answer every produce with a
// non-retriable error until the returned function runs.
func refuseProduces(cluster *kfake.Cluster) func() {
	var mu sync.Mutex
	active := true
	cluster.ControlKey(kmsg.Produce.Int16(), func(req kmsg.Request) (kmsg.Response, error, bool) {
		mu.Lock()
		defer mu.Unlock()
		produceReq, ok := req.(*kmsg.ProduceRequest)
		if !active || !ok {
			return nil, nil, false
		}
		cluster.KeepControl()
		resp, _ := produceReq.ResponseKind().(*kmsg.ProduceResponse)
		for _, topic := range produceReq.Topics {
			respTopic := kmsg.NewProduceResponseTopic()
			respTopic.Topic, respTopic.TopicID = topic.Topic, topic.TopicID
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
		mu.Lock()
		defer mu.Unlock()
		active = false
	}
}

func newRetainKafka(t *testing.T, cluster *kfake.Cluster) *audit.KafkaRecorder {
	t.Helper()
	recorder, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers: cluster.ListenAddrs(), Topic: retainTopic,
		ClientID: "tack-audit-retain-test", ProduceTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewKafkaRecorder: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	return recorder
}

func retainReadEvent(suffix string) audit.Event {
	return audit.Event{
		Verb:    string(audit.VerbNodeRead),
		EventID: uuid.Must(uuid.NewV7()),
		Actor:   audit.Actor{Type: audit.ActorUser, ID: uuid.Must(uuid.NewV7()), Email: "retain+" + suffix + "@example.com"},
		Entity:  audit.Entity{Type: "node", NodeType: "issue", ID: uuid.Must(uuid.NewV7()), Identifier: "ISSUE-" + suffix},
		Context: audit.EventContext{
			OrgID: uuid.Must(uuid.NewV7()), Source: audit.SourceMCP,
			Tool: "tack_get_issue", RequestID: "req-" + suffix,
		},
		Outcome:    audit.OutcomeOK,
		OccurredAt: time.Date(2026, time.October, 27, 3, 21, 0, 0, time.UTC),
	}
}

// consumeEventIDs reads want records from the start of the topic and counts
// each event ID.
func consumeEventIDs(t *testing.T, cluster *kfake.Cluster, want int) map[uuid.UUID]int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.ConsumeTopics(retainTopic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("consumer client: %v", err)
	}
	defer client.Close()
	counts := map[uuid.UUID]int{}
	for read := 0; read < want; {
		fetches := client.PollFetches(ctx)
		if errs := fetches.Errors(); len(errs) > 0 {
			t.Fatalf("consume after %d of %d records: %v", read, want, errs)
		}
		fetches.EachRecord(func(record *kgo.Record) {
			var stored audit.Event
			if err := json.Unmarshal(record.Value, &stored); err != nil {
				t.Fatalf("decode record: %v", err)
			}
			counts[stored.EventID]++
			read++
		})
	}
	return counts
}

// syncedLogs is the default logger's destination during the test. The test
// polls it for the flusher's log lines.
type syncedLogs struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncedLogs) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncedLogs) contains(message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Contains(s.buf.String(), message)
}

// lineContains reports whether one log line contains both message and text.
func (s *syncedLogs) lineContains(message, text string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for line := range strings.SplitSeq(s.buf.String(), "\n") {
		if strings.Contains(line, message) && strings.Contains(line, text) {
			return true
		}
	}
	return false
}

func (s *syncedLogs) waitFor(t *testing.T, message string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for !s.contains(message) {
		select {
		case <-deadline:
			t.Fatalf("the flusher never logged %s", message)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func captureSyncedLogs(t *testing.T) *syncedLogs {
	t.Helper()
	logs := &syncedLogs{mu: sync.Mutex{}, buf: bytes.Buffer{}}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{
		AddSource: false, Level: slog.LevelDebug, ReplaceAttr: nil,
	})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}
