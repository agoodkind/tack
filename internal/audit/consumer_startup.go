package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
)

// auditTopicPartitions is the partition count the consumer ensures for the audit
// topic on a fresh broker. It matches the shardOf width so each (org, shard)
// chain maps to its own partition for parallelism. Correctness does not depend
// on the count because the consumer recomputes the shard from the payload (see
// shardOf); the count is a throughput knob. TACK-305.
const auditTopicPartitions = 256

// pingYugabyteReadyCapWait caps the backoff between Yugabyte readiness pings.
const pingYugabyteReadyCapWait = 5 * time.Second

// pingYugabyteUntilReady retries the Yugabyte ping until it succeeds or ctx is
// canceled, backing off linearly to a cap. A fresh environment starts the
// audit-consumer before `ops audit seed-roles` creates the audit LOGIN roles,
// so the first pings fail SASL auth; waiting for the role to appear beats the
// exit-1 docker crash-loop the consumer used to fall into (TACK-301). Each
// failed attempt logs at Warn, not Error, so a real outage does not flood the
// error stream.
func pingYugabyteUntilReady(ctx context.Context, pool *pgxpool.Pool) error {
	attempts := 0
	for {
		attempts++
		err := pool.Ping(ctx)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			slog.ErrorContext(ctx, "audit.consumer.yugabyte_ping_failed", slog.String("err", err.Error()))
			return fmt.Errorf("audit consumer yugabyte ping: %w", err)
		}
		wait := min(time.Duration(attempts)*time.Second, pingYugabyteReadyCapWait)
		slog.WarnContext(ctx, "audit.consumer.yugabyte_not_ready",
			slog.Int("attempt", attempts),
			slog.String("err", err.Error()),
		)
		select {
		case <-ctx.Done():
			slog.ErrorContext(ctx, "audit.consumer.yugabyte_ping_failed", slog.String("err", ctx.Err().Error()))
			return fmt.Errorf("audit consumer yugabyte ping: %w", ctx.Err())
		case <-time.After(wait):
		}
	}
	if attempts > 1 {
		slog.InfoContext(ctx, "audit.consumer.yugabyte_ready", slog.Int("attempts", attempts))
	}
	return nil
}

// retentionConfigKey is the topic setting that decides how long the broker
// keeps a record nobody has consumed.
const retentionConfigKey = "retention.ms"

// brokerChoosesReplicationFactor is the create-topics sentinel minus one.
const brokerChoosesReplicationFactor = -1

// topicSetting is one topic configuration key and the value written to it.
type topicSetting struct {
	Name  string
	Value string
}

// auditTopicShape is what the consumer requires of the audit topic. Retention
// is always written. ReplicationFactor and MinInSyncReplicas apply only above
// zero; zero keeps the broker's defaults, which is the single-broker stack
// this repository ran before TACK-409 and the value a deploy renders until an
// operator raises it.
//
// The two counts must move in order. A topic with one copy and a two-copy
// minimum rejects every acks=all produce with NOT_ENOUGH_REPLICAS. An operator
// runs `ops queue set-replication` first and raises the minimum second.
type auditTopicShape struct {
	Retention         time.Duration
	ReplicationFactor int
	MinInSyncReplicas int
}

// settings returns the configuration keys this shape declares.
func (shape auditTopicShape) settings() []topicSetting {
	out := []topicSetting{{
		Name:  retentionConfigKey,
		Value: strconv.FormatInt(shape.Retention.Milliseconds(), 10),
	}}
	if shape.MinInSyncReplicas > 0 {
		out = append(out, topicSetting{
			Name:  minInSyncReplicasConfig,
			Value: strconv.Itoa(shape.MinInSyncReplicas),
		})
	}
	return out
}

// createReplicationFactor returns the copy count for a topic this call
// creates. The create-topics field is a signed 16-bit integer. A configured
// value outside that range is a mistake in the environment rather than a state
// of the cluster, and it is refused here.
func (shape auditTopicShape) createReplicationFactor() (int16, error) {
	if shape.ReplicationFactor <= 0 {
		return brokerChoosesReplicationFactor, nil
	}
	if shape.ReplicationFactor > math.MaxInt16 {
		return 0, fmt.Errorf("audit topic replication factor %d is outside the range the request encodes",
			shape.ReplicationFactor)
	}
	return int16(shape.ReplicationFactor), nil
}

// ensureAuditTopic creates the audit topic with auditTopicPartitions partitions
// when it does not already exist, so a fresh broker does not leave the consumer
// fetching a topic that nothing has created (TACK-305). An existing topic keeps
// its partitions and its copy count, and takes the settings above. The topic is
// the buffer for every event the consumer has not yet committed, and the broker
// default of seven days discarded the 2026-07-06 to 07-21 events during a
// consumer outage (TACK-336).
func ensureAuditTopic(ctx context.Context, client *kgo.Client, topic string, shape auditTopicShape) error {
	settings := shape.settings()
	replicationFactor, err := shape.createReplicationFactor()
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.topic_shape_rejected",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return err
	}
	req := kmsg.NewPtrCreateTopicsRequest()
	reqTopic := kmsg.NewCreateTopicsRequestTopic()
	reqTopic.Topic = topic
	reqTopic.NumPartitions = auditTopicPartitions
	reqTopic.ReplicationFactor = replicationFactor
	for index := range settings {
		reqConfig := kmsg.NewCreateTopicsRequestTopicConfig()
		reqConfig.Name = settings[index].Name
		reqConfig.Value = &settings[index].Value
		reqTopic.Configs = append(reqTopic.Configs, reqConfig)
	}
	req.Topics = append(req.Topics, reqTopic)

	resp, err := req.RequestWith(ctx, client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.topic_create_request_failed",
			slog.String("topic", topic),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("audit consumer create topic %s: %w", topic, err)
	}
	for _, respTopic := range resp.Topics {
		if respTopic.ErrorCode == 0 {
			continue
		}
		codeErr := kerr.ErrorForCode(respTopic.ErrorCode)
		if errors.Is(codeErr, kerr.TopicAlreadyExists) {
			// A broker that refuses the alter (an ACL, an older version)
			// leaves the topic as it stands. That is a shorter buffer
			// rather than a stopped consumer. The refusal is logged and
			// the consumer keeps projecting.
			if err := setTopicConfigs(ctx, client, topic, settings); err != nil {
				slog.ErrorContext(ctx, "audit.consumer.topic_settings_unchanged",
					slog.String("topic", topic), slog.String("err", err.Error()))
			}
			continue
		}
		slog.ErrorContext(ctx, "audit.consumer.topic_create_failed",
			slog.String("topic", topic),
			slog.String("err", codeErr.Error()),
		)
		return fmt.Errorf("audit consumer create topic %s: %w", topic, codeErr)
	}
	slog.InfoContext(ctx, "audit.consumer.topic_ensured",
		slog.String("topic", topic),
		slog.Int("partitions", auditTopicPartitions),
		slog.String("retention", shape.Retention.String()),
		slog.Int("replication_factor", shape.ReplicationFactor),
		slog.Int("min_insync_replicas", shape.MinInSyncReplicas),
	)
	return nil
}

// setTopicConfigs is one incremental alter of the named keys against an
// existing topic; every other topic setting is untouched.
func setTopicConfigs(ctx context.Context, client *kgo.Client, topic string, settings []topicSetting) error {
	req := kmsg.NewPtrIncrementalAlterConfigsRequest()
	resource := kmsg.NewIncrementalAlterConfigsRequestResource()
	resource.ResourceType = kmsg.ConfigResourceTypeTopic
	resource.ResourceName = topic
	for index := range settings {
		config := kmsg.NewIncrementalAlterConfigsRequestResourceConfig()
		config.Name = settings[index].Name
		config.Op = kmsg.IncrementalAlterConfigOpSet
		config.Value = &settings[index].Value
		resource.Configs = append(resource.Configs, config)
	}
	req.Resources = append(req.Resources, resource)

	resp, err := req.RequestWith(ctx, client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.topic_settings_request_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return fmt.Errorf("audit consumer set configs on %s: %w", topic, err)
	}
	for _, respResource := range resp.Resources {
		if respResource.ErrorCode == 0 {
			continue
		}
		codeErr := kerr.ErrorForCode(respResource.ErrorCode)
		slog.ErrorContext(ctx, "audit.consumer.topic_settings_failed",
			slog.String("topic", topic), slog.String("err", codeErr.Error()))
		return fmt.Errorf("audit consumer set configs on %s: %w", topic, codeErr)
	}
	slog.InfoContext(ctx, "audit.consumer.topic_settings_set",
		slog.String("topic", topic), slog.Int("count", len(settings)))
	return nil
}
