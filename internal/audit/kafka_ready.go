package audit

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// minInSyncReplicasConfig is the topic config that decides how many in-sync
// replicas an acks=all produce needs before the broker acknowledges it.
const minInSyncReplicasConfig = "min.insync.replicas"

// Ready reports whether this producer can publish its ledger. The client
// connects lazily, so construction alone proves nothing; startup calls this so
// a server that cannot record refuses to serve instead of running unrecorded
// until someone reads a log.
//
// A broker answering is not enough. The topic has to exist, be visible to this
// client, and carry enough in-sync replicas on every partition to satisfy the
// acks this producer demands, because a reachable broker whose topic is
// missing or under-replicated rejects every Record while looking healthy at
// the connection level.
func (k *KafkaRecorder) Ready(ctx context.Context) error {
	if err := k.client.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.kafka.ping_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit kafka ping: %w", err)
	}
	return k.checkTopicWritable(ctx)
}

// checkTopicWritable asks the cluster about the configured topic and demands a
// leader and a sufficient in-sync set on every partition. Auto-creation is
// disabled on the request: a topic conjured by the readiness check would
// report success while the partition count the design fixes had never been
// applied.
func (k *KafkaRecorder) checkTopicWritable(ctx context.Context) error {
	req := kmsg.NewPtrMetadataRequest()
	reqTopic := kmsg.NewMetadataRequestTopic()
	reqTopic.Topic = &k.topic
	req.Topics = append(req.Topics, reqTopic)
	req.AllowAutoTopicCreation = false

	resp, err := req.RequestWith(ctx, k.client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.kafka.topic_metadata_failed",
			slog.String("topic", k.topic), slog.String("err", err.Error()))
		return fmt.Errorf("audit kafka topic %s metadata: %w", k.topic, err)
	}
	if len(resp.Topics) == 0 {
		err := fmt.Errorf("audit kafka topic %s: cluster returned no metadata for it", k.topic)
		slog.ErrorContext(ctx, "audit.kafka.topic_absent",
			slog.String("topic", k.topic), slog.String("err", err.Error()))
		return err
	}
	minInSync, err := k.minInSyncReplicas(ctx)
	if err != nil {
		return err
	}
	for _, respTopic := range resp.Topics {
		if respTopic.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(respTopic.ErrorCode)
			slog.ErrorContext(ctx, "audit.kafka.topic_unwritable",
				slog.String("topic", k.topic), slog.String("err", codeErr.Error()))
			return fmt.Errorf("audit kafka topic %s: %w", k.topic, codeErr)
		}
		if err := k.checkPartitions(ctx, respTopic, minInSync); err != nil {
			return err
		}
	}
	return nil
}

// checkPartitions demands a usable leader and a sufficient in-sync set on
// every partition, not merely one. The producer key hashes an (org, shard)
// pair across the whole partition space, so a single unusable partition fails
// every event that lands on it while the rest of the topic looks healthy. A
// topic with no partitions at all can accept nothing.
func (k *KafkaRecorder) checkPartitions(ctx context.Context, respTopic kmsg.MetadataResponseTopic, minInSync int) error {
	if len(respTopic.Partitions) == 0 {
		err := fmt.Errorf("audit kafka topic %s: reports no partitions", k.topic)
		slog.ErrorContext(ctx, "audit.kafka.topic_partitionless",
			slog.String("topic", k.topic), slog.String("err", err.Error()))
		return err
	}
	for _, partition := range respTopic.Partitions {
		if partition.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(partition.ErrorCode)
			slog.ErrorContext(ctx, "audit.kafka.partition_unwritable",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.String("err", codeErr.Error()))
			return fmt.Errorf("audit kafka topic %s partition %d: %w",
				k.topic, partition.Partition, codeErr)
		}
		if partition.Leader < 0 {
			err := fmt.Errorf("audit kafka topic %s partition %d: no leader",
				k.topic, partition.Partition)
			slog.ErrorContext(ctx, "audit.kafka.partition_leaderless",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.String("err", err.Error()))
			return err
		}
		// This producer requires acks from every in-sync replica, so a
		// partition whose in-sync set is below the topic minimum has each
		// produce rejected with NOT_ENOUGH_REPLICAS even though it reports a
		// leader.
		if len(partition.ISR) < minInSync {
			err := fmt.Errorf("audit kafka topic %s partition %d: %d in-sync replicas, below the topic minimum of %d",
				k.topic, partition.Partition, len(partition.ISR), minInSync)
			slog.ErrorContext(ctx, "audit.kafka.partition_under_replicated",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.Int("in_sync", len(partition.ISR)),
				slog.Int("min_in_sync", minInSync),
				slog.String("err", err.Error()))
			return err
		}
	}
	return nil
}

// minInSyncReplicas reads the topic's effective min.insync.replicas. A value
// this check cannot read is a refusal rather than an assumed default: guessing
// it would let a server start against a topic whose acks requirement it never
// verified, which is the silence this whole path exists to remove.
func (k *KafkaRecorder) minInSyncReplicas(ctx context.Context) (int, error) {
	req := kmsg.NewPtrDescribeConfigsRequest()
	resource := kmsg.NewDescribeConfigsRequestResource()
	resource.ResourceType = kmsg.ConfigResourceTypeTopic
	resource.ResourceName = k.topic
	resource.ConfigNames = []string{minInSyncReplicasConfig}
	req.Resources = append(req.Resources, resource)

	resp, err := req.RequestWith(ctx, k.client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.kafka.topic_config_failed",
			slog.String("topic", k.topic), slog.String("err", err.Error()))
		return 0, fmt.Errorf("audit kafka topic %s config: %w", k.topic, err)
	}
	for _, respResource := range resp.Resources {
		if respResource.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(respResource.ErrorCode)
			slog.ErrorContext(ctx, "audit.kafka.topic_config_rejected",
				slog.String("topic", k.topic), slog.String("err", codeErr.Error()))
			return 0, fmt.Errorf("audit kafka topic %s config: %w", k.topic, codeErr)
		}
		for _, config := range respResource.Configs {
			if config.Name != minInSyncReplicasConfig || config.Value == nil {
				continue
			}
			value, convErr := strconv.Atoi(*config.Value)
			if convErr != nil {
				slog.ErrorContext(ctx, "audit.kafka.topic_config_unreadable",
					slog.String("topic", k.topic),
					slog.String("value", *config.Value),
					slog.String("err", convErr.Error()))
				return 0, fmt.Errorf("audit kafka topic %s %s=%q: %w",
					k.topic, minInSyncReplicasConfig, *config.Value, convErr)
			}
			return value, nil
		}
	}
	err = fmt.Errorf("audit kafka topic %s: cluster reported no %s", k.topic, minInSyncReplicasConfig)
	slog.ErrorContext(ctx, "audit.kafka.topic_config_absent",
		slog.String("topic", k.topic), slog.String("err", err.Error()))
	return 0, err
}
