package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"expvar"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

// captureSlog redirects slog.Default to a JSON handler backed by buf for
// the duration of the test. Returns a teardown that restores the previous
// default.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	buf := &bytes.Buffer{}
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{
		AddSource:   false,
		Level:       slog.LevelDebug,
		ReplaceAttr: nil,
	})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// containsLogEvent reports whether buf has at least one slog JSON line
// whose msg field equals event.
func containsLogEvent(buf *bytes.Buffer, event string) bool {
	for _, line := range strings.Split(buf.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if msg, _ := rec["msg"].(string); msg == event {
			return true
		}
	}
	return false
}

// readMapInt returns the int64 value at key in an [expvar.Map], or 0 when
// the key is absent or not an integer counter. The telemetry package uses
// a private struct for gauge values; falling back to String() parsing
// keeps this helper agnostic to the concrete type.
func readMapInt(m *expvar.Map, key string) int64 {
	v := m.Get(key)
	if v == nil {
		return 0
	}
	if i, ok := v.(*expvar.Int); ok {
		return i.Value()
	}
	n, err := strconv.ParseInt(strings.Trim(v.String(), `"`), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func TestKafkaRecorderProduceErrorEmitsMetricAndLog(t *testing.T) {
	buf := captureSlog(t)

	cluster, err := kfake.NewCluster(
		kfake.NumBrokers(1),
		kfake.SeedTopics(1, testKafkaTopic),
	)
	if err != nil {
		t.Fatalf("kfake.NewCluster: %v", err)
	}

	rec, err := NewKafkaRecorder(KafkaConfig{
		Brokers:        cluster.ListenAddrs(),
		Topic:          testKafkaTopic,
		ClientID:       "tack-audit-monitor-test",
		ProduceTimeout: 1500 * time.Millisecond,
	})
	if err != nil {
		cluster.Close()
		t.Fatalf("NewKafkaRecorder: %v", err)
	}
	t.Cleanup(func() { _ = rec.Close() })

	totalsBefore := readExpvarMapInt(t, "tack_audit_kafka_produce_total", "error")

	cluster.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	produceErr := rec.Record(ctx, makeEvent("monitor-err"))
	if produceErr == nil {
		t.Fatal("Record: expected error when broker is down, got nil")
	}

	totalsAfter := readExpvarMapInt(t, "tack_audit_kafka_produce_total", "error")
	if totalsAfter <= totalsBefore {
		t.Fatalf("audit_kafka_produce_total{error}: got %d, want > %d", totalsAfter, totalsBefore)
	}
	if !containsLogEvent(buf, "kafka.produce.failed") {
		t.Fatalf("expected kafka.produce.failed log; got: %s", buf.String())
	}
}

// fakeFetchPartition is the test-only stand-in we feed to observeLag. It
// uses the same field shape as kgo.FetchTopicPartition by direct value
// construction below.

func TestConsumerObserveLagWarnsAtThreshold(t *testing.T) {
	buf := captureSlog(t)

	c := &Consumer{cfg: ConsumerConfig{
		Brokers:         nil,
		Topic:           testKafkaTopic,
		GroupID:         "g",
		BatchSize:       0,
		PollInterval:    0,
		YugabyteDSN:     "",
		ClickHouseDSN:   "",
		SigningKeyPath:  "",
		NotarizerPeriod: 0,
		LagWarnMessages: 100,
		SummaryEvery:    100,
	}}

	fetches := buildFetchesWithLag(testKafkaTopic, 0, 250, nil)
	c.observeLag(context.Background(), fetches)

	lagKey := testKafkaTopic + ":0"
	gotLag := readExpvarMapInt(t, "tack_audit_consumer_lag_messages", lagKey)
	if gotLag != 250 {
		t.Fatalf("audit_consumer_lag_messages{%s}: got %d, want 250", lagKey, gotLag)
	}
	if !containsLogEvent(buf, "consumer.lag.high") {
		t.Fatalf("expected consumer.lag.high log; got: %s", buf.String())
	}
}

func TestConsumerObserveLagSilentBelowThreshold(t *testing.T) {
	buf := captureSlog(t)

	c := &Consumer{cfg: ConsumerConfig{
		Brokers:         nil,
		Topic:           testKafkaTopic,
		GroupID:         "g",
		BatchSize:       0,
		PollInterval:    0,
		YugabyteDSN:     "",
		ClickHouseDSN:   "",
		SigningKeyPath:  "",
		NotarizerPeriod: 0,
		LagWarnMessages: 1000,
		SummaryEvery:    100,
	}}
	fetches := buildFetchesWithLag(testKafkaTopic, 0, 50, nil)
	c.observeLag(context.Background(), fetches)
	if containsLogEvent(buf, "consumer.lag.high") {
		t.Fatalf("did not expect consumer.lag.high log when lag below threshold; got: %s", buf.String())
	}
}

// buildFetchesWithLag constructs a kgo.Fetches with one topic/partition
// where the broker reports HighWatermark and the supplied records were
// returned. The simulated lag is HighWatermark - (lastRecordOffset + 1)
// when records are present, or HighWatermark when records is empty.
func buildFetchesWithLag(topic string, partition int32, lag int64, records []*kgo.Record) kgo.Fetches {
	var lastOffset int64 = -1
	for _, r := range records {
		if r.Offset > lastOffset {
			lastOffset = r.Offset
		}
	}
	hwm := lag
	if lastOffset >= 0 {
		hwm = lastOffset + 1 + lag
	}
	fp := kgo.FetchPartition{
		Partition:        partition,
		Err:              nil,
		HighWatermark:    hwm,
		LastStableOffset: hwm,
		LogStartOffset:   0,
		Records:          records,
	}
	ft := kgo.FetchTopic{
		Topic:      topic,
		TopicID:    [16]byte{},
		Partitions: []kgo.FetchPartition{fp},
	}
	return kgo.Fetches{kgo.Fetch{Topics: []kgo.FetchTopic{ft}}}
}

func readExpvarMapInt(t *testing.T, mapName, key string) int64 {
	t.Helper()
	v := expvar.Get(mapName)
	if v == nil {
		return 0
	}
	m, ok := v.(*expvar.Map)
	if !ok {
		t.Fatalf("%s is not an expvar.Map", mapName)
	}
	return readMapInt(m, key)
}
