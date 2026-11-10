package audit

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// minInSyncReplicasConfig selects the topic setting for the minimum number
// of in-sync replicas that an acks=all produce requires.
const minInSyncReplicasConfig = "min.insync.replicas"

// Ready checks whether the producer can publish audit events.
//
// The client connects lazily, and construction does not prove connectivity.
// Startup uses Ready to refuse service when audit recording is unavailable.
// A broker can answer while a missing or under-replicated topic rejects
// writes. Ready requires the topic to exist, remain visible to this client,
// and have enough in-sync replicas on every partition for acks=all.
//
// Ready checks these requirements in order:
//
//  1. Ready checks broker connectivity.
//  2. Ready reads topic metadata and its minimum in-sync replica setting.
//  3. Ready checks every partition's leader and in-sync replicas.
func (k *KafkaRecorder) Ready(ctx context.Context) error {
	if err := k.client.Ping(ctx); err != nil {
		slog.ErrorContext(ctx, "audit.kafka.ping_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit kafka ping: %w", err)
	}

	return k.checkTopicWritable(ctx)
}

// checkTopicWritable queries the topic and requires a leader and sufficient
// in-sync replicas on every partition. The request disables automatic topic
// creation. A readiness check that created a topic could succeed without
// applying the design's required partition count.
func (k *KafkaRecorder) checkTopicWritable(ctx context.Context) error {
	req := kmsg.NewPtrMetadataRequest()
	reqTopic := kmsg.NewMetadataRequestTopic()
	reqTopic.Topic = &k.topic
	req.Topics = append(req.Topics, reqTopic)
	req.AllowAutoTopicCreation = false

	resp, err := req.RequestWith(ctx, k.client)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.kafka.topic_metadata_failed",
			slog.String("topic", k.topic),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("audit kafka topic %s metadata: %w", k.topic, err)
	}

	if len(resp.Topics) == 0 {
		err := fmt.Errorf("audit kafka topic %s: cluster returned no metadata for it", k.topic)
		slog.ErrorContext(
			ctx,
			"audit.kafka.topic_absent",
			slog.String("topic", k.topic),
			slog.String("err", err.Error()),
		)

		return err
	}

	minInSync, err := k.minInSyncReplicas(ctx)
	if err != nil {
		return err
	}

	for _, respTopic := range resp.Topics {
		if respTopic.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(respTopic.ErrorCode)
			slog.ErrorContext(
				ctx,
				"audit.kafka.topic_unwritable",
				slog.String("topic", k.topic),
				slog.String("err", codeErr.Error()),
			)

			return fmt.Errorf("audit kafka topic %s: %w", k.topic, codeErr)
		}

		if err := k.checkPartitions(ctx, respTopic, minInSync); err != nil {
			return err
		}
	}

	return nil
}

// checkPartitions requires a usable leader and sufficient in-sync replicas
// on every partition. The producer distributes organization and shard keys
// across all partitions. An unusable partition rejects its assigned events
// even when other partitions remain healthy. A topic without partitions
// cannot accept events.
func (k *KafkaRecorder) checkPartitions(
	ctx context.Context,
	respTopic kmsg.MetadataResponseTopic,
	minInSync int,
) error {
	if len(respTopic.Partitions) == 0 {
		err := fmt.Errorf("audit kafka topic %s: reports no partitions", k.topic)
		slog.ErrorContext(
			ctx,
			"audit.kafka.topic_partitionless",
			slog.String("topic", k.topic),
			slog.String("err", err.Error()),
		)

		return err
	}

	for _, partition := range respTopic.Partitions {
		if partition.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(partition.ErrorCode)
			slog.ErrorContext(
				ctx,
				"audit.kafka.partition_unwritable",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.String("err", codeErr.Error()),
			)

			return fmt.Errorf(
				"audit kafka topic %s partition %d: %w",
				k.topic,
				partition.Partition,
				codeErr,
			)
		}

		if partition.Leader < 0 {
			err := fmt.Errorf(
				"audit kafka topic %s partition %d: no leader",
				k.topic,
				partition.Partition,
			)
			slog.ErrorContext(
				ctx,
				"audit.kafka.partition_leaderless",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.String("err", err.Error()),
			)

			return err
		}

		// The producer requires acknowledgments from every in-sync replica.
		// The broker rejects each produce with NOT_ENOUGH_REPLICAS when the
		// in-sync set falls below the topic minimum, even if a leader exists.
		if len(partition.ISR) < minInSync {
			err := fmt.Errorf(
				"audit kafka topic %s partition %d: %d in-sync replicas, below the topic minimum of %d",
				k.topic,
				partition.Partition,
				len(partition.ISR),
				minInSync,
			)
			slog.ErrorContext(
				ctx,
				"audit.kafka.partition_under_replicated",
				slog.String("topic", k.topic),
				slog.Int("partition", int(partition.Partition)),
				slog.Int("in_sync", len(partition.ISR)),
				slog.Int("min_in_sync", minInSync),
				slog.String("err", err.Error()),
			)

			return err
		}
	}

	return nil
}

// minInSyncReplicas reads the topic's effective min.insync.replicas value.
// An unreadable value fails readiness rather than selecting an assumed
// default. Startup must verify the acknowledgment requirement before the
// server accepts work.
func (k *KafkaRecorder) minInSyncReplicas(ctx context.Context) (int, error) {
	req := kmsg.NewPtrDescribeConfigsRequest()
	resource := kmsg.NewDescribeConfigsRequestResource()
	resource.ResourceType = kmsg.ConfigResourceTypeTopic
	resource.ResourceName = k.topic
	resource.ConfigNames = []string{minInSyncReplicasConfig}
	req.Resources = append(req.Resources, resource)

	resp, err := req.RequestWith(ctx, k.client)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.kafka.topic_config_failed",
			slog.String("topic", k.topic),
			slog.String("err", err.Error()),
		)

		return 0, fmt.Errorf("audit kafka topic %s config: %w", k.topic, err)
	}

	for _, respResource := range resp.Resources {
		if respResource.ErrorCode != 0 {
			codeErr := kerr.ErrorForCode(respResource.ErrorCode)
			slog.ErrorContext(
				ctx,
				"audit.kafka.topic_config_rejected",
				slog.String("topic", k.topic),
				slog.String("err", codeErr.Error()),
			)

			return 0, fmt.Errorf("audit kafka topic %s config: %w", k.topic, codeErr)
		}

		for _, config := range respResource.Configs {
			if config.Name != minInSyncReplicasConfig || config.Value == nil {
				continue
			}

			value, convErr := strconv.Atoi(*config.Value)
			if convErr != nil {
				slog.ErrorContext(
					ctx,
					"audit.kafka.topic_config_unreadable",
					slog.String("topic", k.topic),
					slog.String("value", *config.Value),
					slog.String("err", convErr.Error()),
				)

				return 0, fmt.Errorf(
					"audit kafka topic %s %s=%q: %w",
					k.topic,
					minInSyncReplicasConfig,
					*config.Value,
					convErr,
				)
			}

			return value, nil
		}
	}

	err = fmt.Errorf(
		"audit kafka topic %s: cluster reported no %s",
		k.topic,
		minInSyncReplicasConfig,
	)
	slog.ErrorContext(
		ctx,
		"audit.kafka.topic_config_absent",
		slog.String("topic", k.topic),
		slog.String("err", err.Error()),
	)

	return 0, err
}
