package ops

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/config"
)

// `ops queue set-replication` raises the copy count on an existing topic. Kafka
// fixes a topic's copy count at creation, and only a partition reassignment
// changes it afterwards (TACK-409).

// RunQueueSetReplication moves every partition of a topic onto the requested
// number of brokers. The cluster copies the data in the background and the
// command returns once the controller accepts the plan.
func RunQueueSetReplication(
	ctx context.Context,
	cfg *config.Config,
	topic string,
	replicas int,
	throttleBytes int64,
	sink clispec.ResultSink,
) error {
	client, err := openQueueClient(ctx, cfg)
	if err != nil {
		return err
	}
	defer client.Close()

	brokers, err := readQueueBrokers(ctx, client)
	if err != nil {
		return err
	}
	if replicas > len(brokers) {
		over := fmt.Errorf(
			"ops queue set-replication: %d copies were requested and the cluster reports %d live brokers",
			replicas, len(brokers),
		)
		slog.ErrorContext(ctx, "ops.queue.replication_rejected", slog.String("err", over.Error()))
		return over
	}
	partitions, err := readQueueTopicPartitions(ctx, client, topic)
	if err != nil {
		return err
	}
	brokerIDs := queueBrokerIDs(brokers)
	assignment, err := PlanReplicaAssignment(len(partitions), brokerIDs, replicas)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.plan_rejected", slog.String("err", err.Error()))
		return fmt.Errorf("ops queue placement for topic %s: %w", topic, err)
	}
	if throttleBytes > 0 {
		if err := setQueueThrottle(ctx, client, topic, brokerIDs, throttleBytes); err != nil {
			return err
		}
	}
	if err := submitQueueReassignment(ctx, client, topic, assignment); err != nil {
		return err
	}
	slog.InfoContext(
		ctx, "ops.queue.replication_submitted",
		slog.String("topic", topic),
		slog.Int("partition_count", len(assignment)),
		slog.Int("replicas", replicas),
	)
	return writeQueueOutput(ctx, sink, "topic "+topic+": "+strconv.Itoa(len(assignment))+
		" partitions moved onto "+strconv.Itoa(replicas)+" of brokers "+joinBrokerIDs(brokerIDs))
}

// submitQueueReassignment hands the controller one plan covering every
// partition of the topic.
func submitQueueReassignment(
	ctx context.Context,
	client *kgo.Client,
	topic string,
	assignment [][]int32,
) error {
	req := kmsg.NewPtrAlterPartitionAssignmentsRequest()
	req.TimeoutMillis = queueRequestTimeoutMillis
	reqTopic := kmsg.NewAlterPartitionAssignmentsRequestTopic()
	reqTopic.Topic = topic
	for partition, placement := range assignment {
		reqPartition := kmsg.NewAlterPartitionAssignmentsRequestTopicPartition()
		reqPartition.Partition = int32(partition)
		reqPartition.Replicas = placement
		reqTopic.Partitions = append(reqTopic.Partitions, reqPartition)
	}
	req.Topics = append(req.Topics, reqTopic)

	callCtx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	resp, err := req.RequestWith(callCtx, client)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.reassign_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return fmt.Errorf("ops queue reassign partitions of %s: %w", topic, err)
	}
	if resp.ErrorCode != 0 {
		codeErr := kerr.ErrorForCode(resp.ErrorCode)
		slog.ErrorContext(ctx, "ops.queue.reassign_rejected",
			slog.String("topic", topic), slog.String("err", codeErr.Error()))
		return fmt.Errorf("ops queue reassignment plan for %s was refused: %w", topic, codeErr)
	}
	return readReassignmentOutcome(ctx, resp, topic)
}

// readReassignmentOutcome turns a per-partition refusal into an error naming
// the partition the controller would not move.
func readReassignmentOutcome(
	ctx context.Context,
	resp *kmsg.AlterPartitionAssignmentsResponse,
	topic string,
) error {
	for _, respTopic := range resp.Topics {
		for _, partition := range respTopic.Partitions {
			if partition.ErrorCode == 0 {
				continue
			}
			codeErr := kerr.ErrorForCode(partition.ErrorCode)
			slog.ErrorContext(ctx, "ops.queue.reassign_partition_rejected",
				slog.String("topic", topic),
				slog.Int("partition", int(partition.Partition)),
				slog.String("err", codeErr.Error()))
			return fmt.Errorf("ops queue partition %d of %s stayed where it was: %w",
				partition.Partition, topic, codeErr)
		}
	}
	return nil
}
