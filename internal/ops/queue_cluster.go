package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
)

// Reads against the audit event queue's brokers (TACK-409).

const (
	// queueClientID labels these operator requests in the brokers' logs.
	queueClientID = "tack-ops-queue"
	// queueRequestTimeoutMillis is the same deadline in the milliseconds a
	// broker request states in its own int32 field.
	queueRequestTimeoutMillis = 30_000
	// queueRequestTimeout bounds one request to the brokers. An unreachable
	// cluster otherwise leaves the command waiting with nothing to report.
	queueRequestTimeout = queueRequestTimeoutMillis * time.Millisecond
	// queueConsumerPositionTopic is the topic each consumer group commits its
	// position to.
	queueConsumerPositionTopic = "__consumer_offsets"
	// queueMetadataTopic is the KRaft controller log. DescribeQuorum names it.
	queueMetadataTopic = "__cluster_metadata"
	// queueMetadataPartition is the controller log's only partition.
	queueMetadataPartition = 0
)

// errQueueNoConfiguredBrokers stops a command in an environment that sets no
// AUDIT_KAFKA_BROKERS.
var errQueueNoConfiguredBrokers = errors.New("AUDIT_KAFKA_BROKERS is empty: no broker address to reach")

// errQueueQuorumAbsent marks a DescribeQuorum answer carrying no partition
// state.
var errQueueQuorumAbsent = errors.New("the cluster reported no controller quorum state")

// queueBroker pairs one broker id with the address that broker advertises to
// clients.
type queueBroker struct {
	NodeID  int32
	Address string
}

// queueQuorum is the controller quorum's leader id and its current voter ids.
type queueQuorum struct {
	LeaderID int32
	VoterIDs []int32
}

// openQueueClient opens one client against the configured broker list. Every
// caller closes it on every path.
func openQueueClient(ctx context.Context, cfg *config.Config) (*kgo.Client, error) {
	brokers := audit.SplitBrokers(cfg.AuditKafkaBrokers)
	if len(brokers) == 0 {
		return nil, errQueueNoConfiguredBrokers
	}
	client, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ClientID(queueClientID))
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.client_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops queue client for %d brokers: %w", len(brokers), err)
	}
	return client, nil
}

// readQueueBrokers returns every broker the cluster reports, ordered by id.
func readQueueBrokers(ctx context.Context, client *kgo.Client) ([]queueBroker, error) {
	req := kmsg.NewPtrMetadataRequest()
	req.Topics = []kmsg.MetadataRequestTopic{}
	req.AllowAutoTopicCreation = false

	callCtx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	resp, err := req.RequestWith(callCtx, client)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.brokers_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("ops queue read brokers: %w", err)
	}
	brokers := make([]queueBroker, 0, len(resp.Brokers))
	for _, broker := range resp.Brokers {
		brokers = append(brokers, queueBroker{
			NodeID:  broker.NodeID,
			Address: net.JoinHostPort(broker.Host, strconv.Itoa(int(broker.Port))),
		})
	}
	sort.Slice(brokers, func(first, second int) bool {
		return brokers[first].NodeID < brokers[second].NodeID
	})
	return brokers, nil
}

// queueBrokerIDs reduces the broker list to the ids a replica placement uses.
func queueBrokerIDs(brokers []queueBroker) []int32 {
	identifiers := make([]int32, 0, len(brokers))
	for _, broker := range brokers {
		identifiers = append(identifiers, broker.NodeID)
	}
	return identifiers
}

// readQueueQuorum asks the controller for its quorum state.
func readQueueQuorum(ctx context.Context, client *kgo.Client) (queueQuorum, error) {
	req := kmsg.NewPtrDescribeQuorumRequest()
	reqTopic := kmsg.NewDescribeQuorumRequestTopic()
	reqTopic.Topic = queueMetadataTopic
	reqPartition := kmsg.NewDescribeQuorumRequestTopicPartition()
	reqPartition.Partition = queueMetadataPartition
	reqTopic.Partitions = append(reqTopic.Partitions, reqPartition)
	req.Topics = append(req.Topics, reqTopic)

	callCtx, cancel := context.WithTimeout(ctx, queueRequestTimeout)
	defer cancel()
	resp, err := req.RequestWith(callCtx, client)
	if err != nil {
		slog.ErrorContext(ctx, "ops.queue.quorum_failed", slog.String("err", err.Error()))
		return unknownQuorum(), fmt.Errorf("ops queue describe quorum on %s: %w", queueMetadataTopic, err)
	}
	if resp.ErrorCode != 0 {
		codeErr := kerr.ErrorForCode(resp.ErrorCode)
		slog.ErrorContext(ctx, "ops.queue.quorum_rejected", slog.String("err", codeErr.Error()))
		return unknownQuorum(), fmt.Errorf("ops queue controller quorum: %w", codeErr)
	}
	return firstQuorumPartition(ctx, resp)
}

// unknownQuorum is the value returned beside an error.
func unknownQuorum() queueQuorum {
	return queueQuorum{LeaderID: -1, VoterIDs: nil}
}

// firstQuorumPartition reads the controller log's single partition out of the
// response.
func firstQuorumPartition(ctx context.Context, resp *kmsg.DescribeQuorumResponse) (queueQuorum, error) {
	if len(resp.Topics) == 0 || len(resp.Topics[0].Partitions) == 0 {
		slog.ErrorContext(ctx, "ops.queue.quorum_absent", slog.String("err", errQueueQuorumAbsent.Error()))
		return unknownQuorum(), errQueueQuorumAbsent
	}
	partition := resp.Topics[0].Partitions[0]
	if partition.ErrorCode != 0 {
		codeErr := kerr.ErrorForCode(partition.ErrorCode)
		slog.ErrorContext(ctx, "ops.queue.quorum_partition_rejected",
			slog.String("err", codeErr.Error()))
		return unknownQuorum(), fmt.Errorf("ops queue quorum partition %d: %w",
			partition.Partition, codeErr)
	}
	voters := make([]int32, 0, len(partition.CurrentVoters))
	for _, voter := range partition.CurrentVoters {
		voters = append(voters, voter.ReplicaID)
	}
	return queueQuorum{LeaderID: partition.LeaderID, VoterIDs: voters}, nil
}
