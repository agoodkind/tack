package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// ensureAuditTopic creates the audit topic with auditTopicPartitions partitions
// when it does not already exist, so a fresh broker does not leave the consumer
// fetching a topic that nothing has created (TACK-305). The replication factor
// is left to the broker default (-1) so the same call works at one broker or
// many. An existing topic keeps its partitions and has its retention brought
// to the configured value, because the topic is the buffer that holds every
// event the consumer has not yet committed: the broker default of seven days
// discarded the 2026-07-06 to 07-21 events during a consumer outage (TACK-336).
func ensureAuditTopic(ctx context.Context, client *kgo.Client, topic string, retention time.Duration) error {
	retentionMs := strconv.FormatInt(retention.Milliseconds(), 10)
	req := kmsg.NewPtrCreateTopicsRequest()
	reqTopic := kmsg.NewCreateTopicsRequestTopic()
	reqTopic.Topic = topic
	reqTopic.NumPartitions = auditTopicPartitions
	reqTopic.ReplicationFactor = -1
	reqConfig := kmsg.NewCreateTopicsRequestTopicConfig()
	reqConfig.Name = retentionConfigKey
	reqConfig.Value = &retentionMs
	reqTopic.Configs = append(reqTopic.Configs, reqConfig)
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
			if err := setTopicRetention(ctx, client, topic, retentionMs); err != nil {
				return err
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
		slog.String("retention", retention.String()),
	)
	return nil
}

// setTopicRetention sets retention.ms on an existing topic. It is one
// incremental alter of one key, so every other topic setting is untouched.
func setTopicRetention(ctx context.Context, client *kgo.Client, topic string, retentionMs string) error {
	req := kmsg.NewPtrIncrementalAlterConfigsRequest()
	resource := kmsg.NewIncrementalAlterConfigsRequestResource()
	resource.ResourceType = kmsg.ConfigResourceTypeTopic
	resource.ResourceName = topic
	config := kmsg.NewIncrementalAlterConfigsRequestResourceConfig()
	config.Name = retentionConfigKey
	config.Op = kmsg.IncrementalAlterConfigOpSet
	config.Value = &retentionMs
	resource.Configs = append(resource.Configs, config)
	req.Resources = append(req.Resources, resource)

	resp, err := req.RequestWith(ctx, client)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer.topic_retention_request_failed",
			slog.String("topic", topic), slog.String("err", err.Error()))
		return fmt.Errorf("audit consumer set retention on %s: %w", topic, err)
	}
	for _, respResource := range resp.Resources {
		if respResource.ErrorCode == 0 {
			continue
		}
		codeErr := kerr.ErrorForCode(respResource.ErrorCode)
		slog.ErrorContext(ctx, "audit.consumer.topic_retention_failed",
			slog.String("topic", topic), slog.String("err", codeErr.Error()))
		return fmt.Errorf("audit consumer set retention on %s: %w", topic, codeErr)
	}
	slog.InfoContext(ctx, "audit.consumer.topic_retention_set",
		slog.String("topic", topic), slog.String("retention_ms", retentionMs))
	return nil
}
