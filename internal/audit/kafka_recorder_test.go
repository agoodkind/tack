package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	testKafkaTopic    = "audit.events.v1"
	testFetchDeadline = 10 * time.Second
)

// newFakeCluster spins up a single-broker fake Kafka cluster scoped to t.
func newFakeCluster(t *testing.T) *kfake.Cluster {
	t.Helper()
	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.SeedTopics(1, testKafkaTopic),
	)
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}
	t.Cleanup(cluster.Close)
	return cluster
}

func newKafkaRecorderForTest(t *testing.T, cluster *kfake.Cluster) *KafkaRecorder {
	t.Helper()
	rec, err := NewKafkaRecorder(KafkaConfig{
		Brokers:        cluster.ListenAddrs(),
		Topic:          testKafkaTopic,
		ClientID:       "tack-audit-producer-test",
		ProduceTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewKafkaRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	return rec
}

func makeEvent(suffix string) Event {
	return Event{
		Verb: "node.read",
		Actor: Actor{
			Type:  ActorUser,
			ID:    uuid.New(),
			Email: "tester+" + suffix + "@example.com",
		},
		Entity: Entity{
			Type:       "node",
			NodeType:   "issue",
			ID:         uuid.New(),
			Identifier: "ISSUE-" + suffix,
		},
		Context: EventContext{
			OrgID:     uuid.New(),
			Source:    SourceMCP,
			Tool:      "tack_get_issue",
			RequestID: "req-" + suffix,
		},
		Outcome:    OutcomeOK,
		OccurredAt: time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
	}
}

func consumeRecords(t *testing.T, brokers []string, topic string, want int) []*kgo.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testFetchDeadline)
	defer cancel()
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.ClientID("tack-audit-test-consumer"),
	)
	if err != nil {
		t.Fatalf("consumer client: %v", err)
	}
	defer cl.Close()
	got := make([]*kgo.Record, 0, want)
	for len(got) < want {
		fs := cl.PollFetches(ctx)
		if errs := fs.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if e.Err == context.DeadlineExceeded || e.Err == context.Canceled {
					t.Fatalf("consume timeout: got %d/%d", len(got), want)
				}
			}
			t.Fatalf("consume errors: %v", errs)
		}
		fs.EachRecord(func(r *kgo.Record) {
			got = append(got, r)
		})
	}
	return got
}

func TestKafkaRecorderProducesAndCommits(t *testing.T) {
	cluster := newFakeCluster(t)
	rec := newKafkaRecorderForTest(t, cluster)
	const total = 100
	for i := 0; i < total; i++ {
		ev := makeEvent(strconv.Itoa(i))
		if err := rec.Record(context.Background(), ev); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}
	records := consumeRecords(t, cluster.ListenAddrs(), testKafkaTopic, total)
	if len(records) != total {
		t.Fatalf("consumed %d records, want %d", len(records), total)
	}
}

func TestKafkaRecorderReturnsErrorOnBrokerDown(t *testing.T) {
	cluster := newFakeCluster(t)
	rec, err := NewKafkaRecorder(KafkaConfig{
		Brokers:        cluster.ListenAddrs(),
		Topic:          testKafkaTopic,
		ClientID:       "tack-audit-producer-down-test",
		ProduceTimeout: 1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewKafkaRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })
	cluster.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = rec.Record(ctx, makeEvent("downed"))
	if err == nil {
		t.Fatal("Record: expected error when broker is down, got nil")
	}
}

func TestKafkaRecorderPayloadShape(t *testing.T) {
	cluster := newFakeCluster(t)
	rec := newKafkaRecorderForTest(t, cluster)
	ev := makeEvent("shape")
	if err := rec.Record(context.Background(), ev); err != nil {
		t.Fatalf("Record: %v", err)
	}
	records := consumeRecords(t, cluster.ListenAddrs(), testKafkaTopic, 1)
	if len(records) != 1 {
		t.Fatalf("consumed %d records, want 1", len(records))
	}
	var got Event
	if err := json.Unmarshal(records[0].Value, &got); err != nil {
		t.Fatalf("unmarshal value: %v", err)
	}
	if got.Verb != ev.Verb {
		t.Errorf("verb: got %q, want %q", got.Verb, ev.Verb)
	}
	if got.Actor.ID != ev.Actor.ID {
		t.Errorf("actor.id: got %v, want %v", got.Actor.ID, ev.Actor.ID)
	}
	if got.Entity.ID != ev.Entity.ID {
		t.Errorf("entity.id: got %v, want %v", got.Entity.ID, ev.Entity.ID)
	}
	if got.Context.OrgID != ev.Context.OrgID {
		t.Errorf("context.org_id: got %v, want %v", got.Context.OrgID, ev.Context.OrgID)
	}
	if got.Context.Tool != ev.Context.Tool {
		t.Errorf("context.tool: got %q, want %q", got.Context.Tool, ev.Context.Tool)
	}
	if !got.OccurredAt.Equal(ev.OccurredAt) {
		t.Errorf("occurred_at: got %v, want %v", got.OccurredAt, ev.OccurredAt)
	}
	if got.Outcome != ev.Outcome {
		t.Errorf("outcome: got %q, want %q", got.Outcome, ev.Outcome)
	}
}

func TestKafkaRecorderRequiresBrokers(t *testing.T) {
	if _, err := NewKafkaRecorder(KafkaConfig{Topic: testKafkaTopic}); err == nil {
		t.Fatal("NewKafkaRecorder: expected error when brokers are empty")
	}
	if _, err := NewKafkaRecorder(KafkaConfig{Brokers: []string{"127.0.0.1:9092"}}); err == nil {
		t.Fatal("NewKafkaRecorder: expected error when topic is empty")
	}
}

func TestApplyKafkaDefaultsUsesProduceTimeoutForNonpositiveValues(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		got := applyKafkaDefaults(KafkaConfig{ProduceTimeout: timeout})
		if got.ProduceTimeout != 15*time.Second {
			t.Fatalf("ProduceTimeout %s: got %s, want 15s", timeout, got.ProduceTimeout)
		}
	}
}

func TestSplitBrokers(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a:9092", []string{"a:9092"}},
		{"a:9092,b:9092", []string{"a:9092", "b:9092"}},
		{" a:9092 , , b:9092 ", []string{"a:9092", "b:9092"}},
	}
	for _, tc := range cases {
		got := SplitBrokers(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("SplitBrokers(%q): len got %d, want %d", tc.in, len(got), len(tc.want))
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("SplitBrokers(%q)[%d]: got %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}
