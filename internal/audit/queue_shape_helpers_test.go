package audit

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// newQueueShapeClient opens one client against the whole bootstrap list for
// the topic requests the assertions make.
func newQueueShapeClient(t *testing.T, brokers []string) *kgo.Client {
	t.Helper()
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ClientID("tack-audit-shape-admin"),
	)
	if err != nil {
		t.Fatalf("admin client: %v", err)
	}
	t.Cleanup(client.Close)
	return client
}

// produceQueueShapeRun records one counted run through the production
// recorder and returns the event ids it wrote. A refused produce fails the
// test at once, during the outage as much as before it.
func produceQueueShapeRun(t *testing.T, recorder *KafkaRecorder, label string) []uuid.UUID {
	t.Helper()
	written := make([]uuid.UUID, 0, queueShapeRunSize)
	for index := range queueShapeRunSize {
		event := makeEvent(label + "-" + strconv.Itoa(index))
		event.EventID = uuid.Must(uuid.NewV7())
		if err := recorder.Record(context.Background(), event); err != nil {
			t.Fatalf("record %s event %d: %v", label, index, err)
		}
		written = append(written, event.EventID)
	}
	return written
}

// assertQueueShapeReplicas demands three replicas on every partition of the
// topic, read from cluster metadata.
func assertQueueShapeReplicas(t *testing.T, client *kgo.Client, topic string) {
	t.Helper()
	req := kmsg.NewPtrMetadataRequest()
	reqTopic := kmsg.NewMetadataRequestTopic()
	reqTopic.Topic = &topic
	req.Topics = append(req.Topics, reqTopic)
	req.AllowAutoTopicCreation = false
	resp, err := req.RequestWith(context.Background(), client)
	if err != nil {
		t.Fatalf("metadata for %s: %v", topic, err)
	}
	if len(resp.Topics) != 1 {
		t.Fatalf("metadata for %s returned %d topics, want 1", topic, len(resp.Topics))
	}
	partitions := resp.Topics[0].Partitions
	if len(partitions) != auditTopicPartitions {
		t.Fatalf("topic %s has %d partitions, want %d", topic, len(partitions), auditTopicPartitions)
	}
	for _, partition := range partitions {
		if len(partition.Replicas) != queueShapeReplicas {
			t.Fatalf("topic %s partition %d has %d replicas, want %d",
				topic, partition.Partition, len(partition.Replicas), queueShapeReplicas)
		}
	}
}

// assertQueueShapeMinInSync demands the effective minimum of two, sourced
// from the topic's own configuration. A minimum inherited from a broker
// default would leave the audit topic at the cluster-wide value rather than
// the one ensureAuditTopic wrote.
func assertQueueShapeMinInSync(t *testing.T, client *kgo.Client, topic string) {
	t.Helper()
	req := kmsg.NewPtrDescribeConfigsRequest()
	resource := kmsg.NewDescribeConfigsRequestResource()
	resource.ResourceType = kmsg.ConfigResourceTypeTopic
	resource.ResourceName = topic
	resource.ConfigNames = []string{minInSyncReplicasConfig}
	req.Resources = append(req.Resources, resource)
	resp, err := req.RequestWith(context.Background(), client)
	if err != nil {
		t.Fatalf("describe configs for %s: %v", topic, err)
	}
	for _, respResource := range resp.Resources {
		for _, config := range respResource.Configs {
			if config.Name != minInSyncReplicasConfig {
				continue
			}
			assertQueueShapeConfig(t, topic, config)
			return
		}
	}
	t.Fatalf("topic %s reports no %s", topic, minInSyncReplicasConfig)
}

// assertQueueShapeConfig judges one reported configuration entry.
func assertQueueShapeConfig(t *testing.T, topic string, config kmsg.DescribeConfigsResponseResourceConfig) {
	t.Helper()
	reported := "absent"
	if config.Value != nil {
		reported = *config.Value
	}
	if reported != strconv.Itoa(queueShapeMinInSync) {
		t.Fatalf("topic %s reports %s=%s, want %d", topic, config.Name, reported, queueShapeMinInSync)
	}
	if config.Source != kmsg.ConfigSourceDynamicTopicConfig {
		t.Fatalf("topic %s takes %s from %s, want the topic's own configuration",
			topic, config.Name, config.Source)
	}
}

// assertQueueShapeDelivered reads the topic from its first offset and demands
// one record for each produced event id.
func assertQueueShapeDelivered(t *testing.T, brokers []string, topic string, produced []uuid.UUID) {
	t.Helper()
	records := consumeQueueShapeTopic(t, brokers, topic, len(produced))
	if len(records) != len(produced) {
		t.Fatalf("topic %s returned %d records, want %d", topic, len(records), len(produced))
	}
	appearances := make(map[uuid.UUID]int, len(produced))
	for _, record := range records {
		var event Event
		if err := json.Unmarshal(record.Value, &event); err != nil {
			t.Fatalf("decode a record of %s: %v", topic, err)
		}
		appearances[event.EventID]++
	}
	for _, eventID := range produced {
		if appearances[eventID] != 1 {
			t.Fatalf("event %s appears %d times, want once", eventID, appearances[eventID])
		}
	}
}

// consumeQueueShapeTopic reads want records from the start of the topic.
func consumeQueueShapeTopic(t *testing.T, brokers []string, topic string, want int) []*kgo.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queueShapeConsumeTimeout)
	defer cancel()
	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.ClientID("tack-audit-shape-consumer"),
	)
	if err != nil {
		t.Fatalf("consumer client: %v", err)
	}
	defer client.Close()
	read := make([]*kgo.Record, 0, want)
	for len(read) < want {
		fetches := client.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("topic %s gave %d records of %d before the deadline", topic, len(read), want)
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			t.Fatalf("fetch from %s: %v", topic, errs)
		}
		fetches.EachRecord(func(record *kgo.Record) {
			read = append(read, record)
		})
	}
	return read
}
