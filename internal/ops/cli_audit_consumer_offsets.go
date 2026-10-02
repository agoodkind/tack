package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/clock"
)

// auditOutboxCountPage bounds one FoundationDB outbox read while counting.
const auditOutboxCountPage = 500

// auditPartitionLag is the committed lag of one topic partition for one
// consumer group. Lag is the high-water mark minus the committed offset.
type auditPartitionLag struct {
	Group       string `json:"consumer_group"`
	Partition   int32  `json:"partition"`
	Committed   int64  `json:"committed_offset"`
	CommittedAt string `json:"committed_at,omitempty"`
	HighWater   int64  `json:"high_water_mark"`
	Lag         int64  `json:"lag"`
}

type auditConsumerOffsetsReport struct {
	clispec.ResultMarker
	Command                   string              `json:"command"`
	ReadAt                    string              `json:"read_at"`
	Topic                     string              `json:"topic"`
	Partitions                []auditPartitionLag `json:"partitions"`
	TotalLag                  int64               `json:"total_lag"`
	OperatorOutboxRows        int64               `json:"operator_outbox_rows"`
	OperatorOutboxOldest      string              `json:"operator_outbox_oldest,omitempty"`
	FoundationDBOutboxEntries int                 `json:"foundationdb_outbox_entries"`
}

// readAuditConsumerOffsets reads the committed offsets, the high-water marks,
// and both operator outbox remainders, and builds the report.
func readAuditConsumerOffsets(ctx context.Context, f *cli.Factory) (auditConsumerOffsetsReport, error) {
	report := auditConsumerOffsetsReport{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.audit.consumer_offsets",
		ReadAt: clock.Now().UTC().Format(time.RFC3339Nano), Topic: f.Cfg.AuditKafkaTopic,
		Partitions: nil, TotalLag: 0, OperatorOutboxRows: 0, OperatorOutboxOldest: "", FoundationDBOutboxEntries: 0,
	}
	brokers := strings.TrimSpace(f.Cfg.AuditKafkaBrokers)
	if brokers == "" || strings.TrimSpace(f.Cfg.AuditReaderDSN) == "" {
		err := errors.New("ops audit consumer-offsets needs AUDIT_READER_DSN and AUDIT_KAFKA_BROKERS")
		slog.ErrorContext(ctx, "audit.consumer_offsets.config_missing", slog.String("err", err.Error()))
		return report, err
	}
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer_offsets.reader_failed", slog.String("err", err.Error()))
		return report, fmt.Errorf("open the ledger reader: %w", err)
	}
	defer reader.Close()
	offsets, err := reader.ConsumerOffsets(ctx)
	if err != nil {
		return report, consumerOffsetsFailure(ctx, "read committed offsets", err)
	}
	marks, err := audit.TopicHighWaterMarks(ctx, audit.SplitBrokers(brokers), report.Topic)
	if err != nil {
		return report, consumerOffsetsFailure(ctx, "read high-water marks", err)
	}
	report.Partitions, report.TotalLag = partitionLags(report.Topic, offsets, marks)
	remainder, err := reader.OperatorOutboxRemainder(ctx)
	if err != nil {
		return report, consumerOffsetsFailure(ctx, "read the operator outbox remainder", err)
	}
	report.OperatorOutboxRows = remainder.Rows
	if remainder.Oldest != nil {
		report.OperatorOutboxOldest = remainder.Oldest.UTC().Format(time.RFC3339Nano)
	}
	report.FoundationDBOutboxEntries, err = countFoundationDBOutbox(ctx, f)
	return report, err
}

// partitionLags pairs every partition high-water mark with the committed
// offset of each consumer group on topic. A partition without a committed row
// has committed offset 0.
func partitionLags(topic string, offsets []audit.ConsumerOffset, marks map[int32]int64) ([]auditPartitionLag, int64) {
	committed := map[string]map[int32]audit.ConsumerOffset{}
	for _, offset := range offsets {
		if offset.Topic != topic {
			continue
		}
		if committed[offset.Group] == nil {
			committed[offset.Group] = map[int32]audit.ConsumerOffset{}
		}
		committed[offset.Group][offset.Partition] = offset
	}
	if len(committed) == 0 {
		committed[""] = map[int32]audit.ConsumerOffset{}
	}
	partitions := slices.Sorted(maps.Keys(marks))
	lags := make([]auditPartitionLag, 0, len(committed)*len(partitions))
	total := int64(0)
	for _, group := range slices.Sorted(maps.Keys(committed)) {
		for _, partition := range partitions {
			row, found := committed[group][partition]
			lag := auditPartitionLag{Group: group, Partition: partition, Committed: 0, CommittedAt: "", HighWater: marks[partition], Lag: 0}
			if found {
				lag.Committed, lag.CommittedAt = row.Offset, row.UpdatedAt.UTC().Format(time.RFC3339Nano)
			}
			lag.Lag = max(lag.HighWater-lag.Committed, 0)
			total += lag.Lag
			lags = append(lags, lag)
		}
	}
	return lags, total
}

// countFoundationDBOutbox counts the FoundationDB operator outbox entries by
// reading them in pages from the start of the outbox.
func countFoundationDBOutbox(ctx context.Context, f *cli.Factory) (int, error) {
	env, err := NewEnv(ctx, f.Cfg)
	if err != nil {
		slog.ErrorContext(ctx, "audit.consumer_offsets.environment_failed", slog.String("err", err.Error()))
		return 0, fmt.Errorf("open the FoundationDB outbox: %w", err)
	}
	defer env.Close()
	total := 0
	var mark []byte
	for {
		entries, err := env.Stores.OpsOutbox.ReadOutboxFrom(ctx, mark, auditOutboxCountPage)
		if err != nil {
			slog.ErrorContext(ctx, "audit.consumer_offsets.outbox_read_failed", slog.String("err", err.Error()))
			return total, fmt.Errorf("count the FoundationDB operator outbox: %w", err)
		}
		total += len(entries)
		if len(entries) < auditOutboxCountPage {
			return total, nil
		}
		mark = entries[len(entries)-1].Mark
	}
}

// consumerOffsetsFailure logs one failed read of the report and returns it
// wrapped with the operation.
func consumerOffsetsFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("ops audit consumer-offsets: %s: %w", operation, err)
	slog.ErrorContext(ctx, "audit.consumer_offsets.failed", slog.String("err", wrapped.Error()))
	return wrapped
}
