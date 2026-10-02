package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// dbPlanProjectionWait is the default bound on the wait of plan close for
	// the relay to empty the operator outbox of its events at the start and
	// for the audit consumer to commit past the audit topic high-water marks.
	dbPlanProjectionWait = 2 * time.Minute
	// dbPlanProjectionPoll is the interval between reads of public.ops_outbox
	// or audit.consumer_offsets during that wait, and between reads of the
	// plan rows while a planned statement waits for the open row.
	dbPlanProjectionPoll = 500 * time.Millisecond
)

// requireDBPlanProjectionConfig returns an error that lists every setting
// plan close needs to read the operator outbox, the audit topic, the consumer
// offsets, and the ledger and that cfg leaves empty.
func requireDBPlanProjectionConfig(cfg *config.Config) error {
	settings := []struct {
		name  string
		value string
	}{
		{name: "AUDIT_KAFKA_BROKERS", value: cfg.AuditKafkaBrokers},
		{name: "AUDIT_KAFKA_TOPIC", value: cfg.AuditKafkaTopic},
		{name: "AUDIT_CONSUMER_GROUP_ID", value: cfg.AuditConsumerGroupID},
		{name: "AUDIT_READER_DSN", value: cfg.AuditReaderDSN},
	}
	var missing []string
	for _, setting := range settings {
		if strings.TrimSpace(setting.value) == "" {
			missing = append(missing, setting.name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return errors.New("ops db plan close reads the operator outbox, the audit topic, the consumer offsets, " +
		"and the ledger; set " + strings.Join(missing, ", "))
}

// parseDBPlanWait parses --wait as a positive Go duration. An empty value
// returns dbPlanProjectionWait.
func parseDBPlanWait(ctx context.Context, text string) (time.Duration, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return dbPlanProjectionWait, nil
	}
	wait, err := time.ParseDuration(trimmed)
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.bad_wait", slog.String("err", err.Error()))
		return 0, fmt.Errorf("--wait %q is not a Go duration: %w", text, err)
	}
	if wait <= 0 {
		return 0, errors.New("--wait must be positive, got " + wait.String())
	}
	return wait, nil
}

// awaitDBPlanProjection waits, within one bound of wait, until every plan row
// written before the call is in audit.events or audit.events_dlq. It first
// waits until the relay has removed each event that public.ops_outbox
// contained at the start. It then reads the high-water mark of every
// partition of the audit topic and reads audit.consumer_offsets every
// dbPlanProjectionPoll until the consumer group has a committed offset at or
// past each nonzero mark. It returns an error when wait passes first. Every
// record below a committed offset is in audit.events or audit.events_dlq.
func awaitDBPlanProjection(ctx context.Context, cfg *config.Config, planID uuid.UUID, wait time.Duration) error {
	reader, err := audit.NewReader(ctx, cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.offsets_reader_failed", slog.String("plan_id", planID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("open the ledger reader to close plan %s: %w", planID, err)
	}
	defer reader.Close()
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := awaitDBPlanOutboxDrain(waitCtx, reader, planID, wait); err != nil {
		return err
	}
	topic, group := cfg.AuditKafkaTopic, cfg.AuditConsumerGroupID
	marks, err := audit.TopicHighWaterMarks(waitCtx, audit.SplitBrokers(cfg.AuditKafkaBrokers), topic)
	if err != nil {
		slog.ErrorContext(ctx, "db.plan.high_water_failed", slog.String("plan_id", planID.String()), slog.String("err", err.Error()))
		return fmt.Errorf("read the high-water marks of %s to close plan %s: %w", topic, planID, err)
	}
	ticker := time.NewTicker(dbPlanProjectionPoll)
	defer ticker.Stop()
	behind, lastRead := slices.Sorted(maps.Keys(marks)), "none"
	for {
		offsets, readErr := reader.ConsumerOffsets(waitCtx)
		if readErr == nil {
			behind = dbPlanPartitionsBehind(group, topic, offsets, marks)
		} else {
			lastRead = readErr.Error()
		}
		if readErr == nil && len(behind) == 0 {
			telemetry.L(ctx).InfoContext(ctx, "db.plan.projection_passed",
				slog.String("plan_id", planID.String()), slog.String("group", group), slog.String("topic", topic))
			return nil
		}
		select {
		case <-waitCtx.Done():
			return dbPlanProjectionTimeout(ctx, planID, group, topic, wait, behind, lastRead)
		case <-ticker.C:
		}
	}
}

// dbPlanPartitionsBehind returns the partitions of topic with a nonzero
// high-water mark above the committed offset of group.
func dbPlanPartitionsBehind(group, topic string, offsets []audit.ConsumerOffset, marks map[int32]int64) []int32 {
	committed := map[int32]int64{}
	for _, offset := range offsets {
		if offset.Group == group && offset.Topic == topic {
			committed[offset.Partition] = offset.Offset
		}
	}
	var behind []int32
	for _, partition := range slices.Sorted(maps.Keys(marks)) {
		if marks[partition] > 0 && committed[partition] < marks[partition] {
			behind = append(behind, partition)
		}
	}
	return behind
}

// dbPlanProjectionTimeout logs and returns the error of a wait that passed
// its bound.
func dbPlanProjectionTimeout(
	ctx context.Context,
	planID uuid.UUID,
	group, topic string,
	wait time.Duration,
	behind []int32,
	lastRead string,
) error {
	partitions := make([]string, 0, len(behind))
	for _, partition := range behind {
		partitions = append(partitions, strconv.Itoa(int(partition)))
	}
	err := errors.New("consumer group " + group + " did not commit past the high-water marks of " + topic +
		" within " + wait.String() + "; partitions behind: " + strings.Join(partitions, ", ") +
		"; last consumer offset read error: " + lastRead)
	slog.ErrorContext(ctx, "db.plan.projection_timeout", slog.String("plan_id", planID.String()), slog.String("err", err.Error()))
	return err
}
