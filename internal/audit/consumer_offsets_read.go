package audit

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// latestOffsetTimestamp asks ListOffsets for the offset after the last record
// of a partition, the high-water mark.
const latestOffsetTimestamp = -1

// ConsumerOffset is one committed row of audit.consumer_offsets. Offset is the
// offset of the next record the consumer will project.
type ConsumerOffset struct {
	Group     string
	Topic     string
	Partition int32
	Offset    int64
	UpdatedAt time.Time
}

// ConsumerOffsets reads every committed consumer offset through the ledger
// reader. The consumer writes each row in the transaction that appends the
// batch. Every record before a row's committed offset is in the ledger or in
// the dead-letter table.
func (r *Reader) ConsumerOffsets(ctx context.Context) ([]ConsumerOffset, error) {
	if r == nil || r.pool == nil {
		return nil, fmt.Errorf("audit reader not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT consumer_group, topic, partition, "offset", updated_at
		  FROM audit.consumer_offsets
		 ORDER BY consumer_group, topic, partition
	`)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer_offsets.read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read consumer offsets: %w", err)
	}
	defer rows.Close()
	offsets := make([]ConsumerOffset, 0)
	for rows.Next() {
		var row ConsumerOffset
		if err := rows.Scan(&row.Group, &row.Topic, &row.Partition, &row.Offset, &row.UpdatedAt); err != nil {
			slog.ErrorContext(ctx, "audit.consumer_offsets.scan_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan consumer offset: %w", err)
		}
		offsets = append(offsets, row)
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.consumer_offsets.rows_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("read consumer offset rows: %w", err)
	}
	return offsets, nil
}

// TopicHighWaterMarks returns the high-water mark of every partition of topic,
// read from the brokers with one metadata request and one ListOffsets request.
func TopicHighWaterMarks(ctx context.Context, brokers []string, topic string) (map[int32]int64, error) {
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID("tack-audit-consumer-offsets"))
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer_offsets.kafka_client_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open kafka client for topic %s: %w", topic, err)
	}
	defer client.Close()
	partitions, err := topicPartitions(ctx, client, topic)
	if err != nil {
		return nil, err
	}
	request := kmsg.NewPtrListOffsetsRequest()
	request.ReplicaID = -1
	requestTopic := kmsg.NewListOffsetsRequestTopic()
	requestTopic.Topic = topic
	for _, partition := range partitions {
		requestPartition := kmsg.NewListOffsetsRequestTopicPartition()
		requestPartition.Partition = partition
		requestPartition.Timestamp = latestOffsetTimestamp
		requestTopic.Partitions = append(requestTopic.Partitions, requestPartition)
	}
	request.Topics = append(request.Topics, requestTopic)
	response, err := request.RequestWith(ctx, client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer_offsets.list_offsets_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return nil, fmt.Errorf("list offsets of topic %s: %w", topic, err)
	}
	marks := make(map[int32]int64, len(partitions))
	for _, responseTopic := range response.Topics {
		for _, responsePartition := range responseTopic.Partitions {
			if responsePartition.ErrorCode != 0 {
				codeErr := kerr.ErrorForCode(responsePartition.ErrorCode)
				slog.ErrorContext(ctx, "audit.consumer_offsets.partition_offset_failed",
					slog.String("topic", topic), slog.Int("partition", int(responsePartition.Partition)),
					slog.String("err", codeErr.Error()))
				return nil, fmt.Errorf("list offset of topic %s partition %d: %w", topic, responsePartition.Partition, codeErr)
			}
			marks[responsePartition.Partition] = responsePartition.Offset
		}
	}
	return marks, nil
}

// topicPartitions lists the partition numbers of topic from cluster metadata.
// It does not create the topic.
func topicPartitions(ctx context.Context, client *kgo.Client, topic string) ([]int32, error) {
	request := kmsg.NewPtrMetadataRequest()
	requestTopic := kmsg.NewMetadataRequestTopic()
	requestTopic.Topic = &topic
	request.Topics = append(request.Topics, requestTopic)
	request.AllowAutoTopicCreation = false
	response, err := request.RequestWith(ctx, client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer_offsets.metadata_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return nil, fmt.Errorf("read metadata of topic %s: %w", topic, err)
	}
	partitions := make([]int32, 0)
	for _, responseTopic := range response.Topics {
		if responseTopic.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(responseTopic.ErrorCode)
			slog.ErrorContext(ctx, "audit.consumer_offsets.topic_unavailable",
				slog.String("topic", topic), slog.String("err", codeErr.Error()))
			return nil, fmt.Errorf("metadata of topic %s: %w", topic, codeErr)
		}
		for _, partition := range responseTopic.Partitions {
			partitions = append(partitions, partition.Partition)
		}
	}
	return partitions, nil
}
