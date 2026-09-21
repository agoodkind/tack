package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// queueMinInSyncKey is the topic setting that decides how many in-sync copies
// an acks=all produce needs before a broker acknowledges it.
const queueMinInSyncKey = "min.insync.replicas"

// queueTopicShape is what one topic's partitions report about their copies.
// SmallestReplicaCount and LargestReplicaCount differ while a reassignment is
// still moving partitions.
type queueTopicShape struct {
	Topic                string
	PartitionCount       int
	SmallestReplicaCount int
	LargestReplicaCount  int
	SmallestInSyncCount  int
	MinInSyncReplicas    int
}

// readQueueTopicPartitions returns the partition records for one topic.
// Auto-creation stays off: a topic conjured by an operator's reading would
// report a shape nobody asked for.
func readQueueTopicPartitions(
	ctx context.Context,
	client *kgo.Client,
	topic string,
) ([]kmsg.MetadataResponseTopicPartition, error) {
	req := kmsg.NewPtrMetadataRequest()
	reqTopic := kmsg.NewMetadataRequestTopic()
	reqTopic.Topic = &topic
	req.Topics = append(req.Topics, reqTopic)
	req.AllowAutoTopicCreation = false

	callCtx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	resp, err := req.RequestWith(callCtx, client)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.topic_metadata_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops queue metadata for topic %s: %w", topic, err)
	}
	if len(resp.Topics) == 0 {
		missing := fmt.Errorf("ops queue topic %s: the cluster returned no metadata for it", topic)
		slog.ErrorContext(ctx, "ops.queue.topic_absent",
			slog.String("topic", topic), slog.String("err", missing.Error()))
		return nil, missing
	}
	respTopic := resp.Topics[0]
	if respTopic.ErrorCode != 0 {
		codeErr := kerr.ErrorForCode(respTopic.ErrorCode)
		slog.ErrorContext(ctx, "ops.queue.topic_unreadable",
			slog.String("topic", topic), slog.String("err", codeErr.Error()))
		return nil, fmt.Errorf("ops queue topic %s is unreadable: %w", topic, codeErr)
	}
	return respTopic.Partitions, nil
}

// readQueueTopicShape summarizes one topic's copies and its acks requirement.
func readQueueTopicShape(ctx context.Context, client *kgo.Client, topic string) (queueTopicShape, error) {
	blank := queueTopicShape{
		Topic: topic, PartitionCount: 0, SmallestReplicaCount: 0,
		LargestReplicaCount: 0, SmallestInSyncCount: 0, MinInSyncReplicas: 0,
	}
	partitions, err := readQueueTopicPartitions(ctx, client, topic)
	if err != nil {
		return blank, err
	}
	minimum, err := readQueueMinInSync(ctx, client, topic)
	if err != nil {
		return blank, err
	}
	shape := blank
	shape.PartitionCount = len(partitions)
	shape.MinInSyncReplicas = minimum
	for index, partition := range partitions {
		replicaCount := len(partition.Replicas)
		inSyncCount := len(partition.ISR)
		if index == 0 || replicaCount < shape.SmallestReplicaCount {
			shape.SmallestReplicaCount = replicaCount
		}
		if replicaCount > shape.LargestReplicaCount {
			shape.LargestReplicaCount = replicaCount
		}
		if index == 0 || inSyncCount < shape.SmallestInSyncCount {
			shape.SmallestInSyncCount = inSyncCount
		}
	}
	return shape, nil
}

// readQueueMinInSync reads one topic's effective min.insync.replicas. An
// unreadable value is a refusal rather than an assumed default.
func readQueueMinInSync(ctx context.Context, client *kgo.Client, topic string) (int, error) {
	req := kmsg.NewPtrDescribeConfigsRequest()
	resource := kmsg.NewDescribeConfigsRequestResource()
	resource.ResourceType = kmsg.ConfigResourceTypeTopic
	resource.ResourceName = topic
	resource.ConfigNames = []string{queueMinInSyncKey}
	req.Resources = append(req.Resources, resource)

	callCtx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	resp, err := req.RequestWith(callCtx, client)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.topic_config_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return 0, fmt.Errorf("ops queue describe configs of %s: %w", topic, err)
	}
	return firstMinInSyncValue(ctx, resp, topic)
}

// firstMinInSyncValue pulls the one config value out of the describe answer.
func firstMinInSyncValue(ctx context.Context, resp *kmsg.DescribeConfigsResponse, topic string) (int, error) {
	for _, respResource := range resp.Resources {
		if respResource.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(respResource.ErrorCode)
			slog.ErrorContext(ctx, "ops.queue.topic_config_rejected",
				slog.String("topic", topic), slog.String("err", codeErr.Error()))
			return 0, fmt.Errorf("ops queue config read on %s was refused: %w", topic, codeErr)
		}
		for _, config := range respResource.Configs {
			if config.Name != queueMinInSyncKey || config.Value == nil {
				continue
			}
			value, convErr := strconv.Atoi(*config.Value)
			if convErr != nil {
				slog.ErrorContext(ctx, "ops.queue.topic_config_unreadable",
					slog.String("topic", topic), slog.String("value", *config.Value),
					slog.String("err", convErr.Error()))
				return 0, fmt.Errorf("ops queue %s %s=%q: %w",
					topic, queueMinInSyncKey, *config.Value, convErr)
			}
			return value, nil
		}
	}
	absent := fmt.Errorf("ops queue %s: the cluster reported no %s", topic, queueMinInSyncKey)
	slog.ErrorContext(ctx, "ops.queue.topic_config_absent",
		slog.String("topic", topic), slog.String("err", absent.Error()))
	return 0, absent
}
