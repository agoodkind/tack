package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// The dead-letter table holds every audit event the consumer could not commit
// to the ledger. These commands let an operator see what accumulated and send
// it back through the audit topic once the cause is gone; the consumer, as
// the ledger's one writer, lands each event and clears its row (TACK-336,
// TACK-240).

var auditDLQGroup = &clispec.Group{Use: "dlq", Short: "Inspect and replay the audit dead-letter table", Long: "", Parent: auditOpsGroup}

// defaultDLQReplayLimit bounds one replay run so an operator reads the
// consumer's answer before sending the rest.
const defaultDLQReplayLimit = 500

type auditDLQInspectInput struct {
	clispec.InputMarker
}

type auditDLQReplayInput struct {
	clispec.InputMarker
	Limit int
}

type auditDLQSummaryResult struct {
	Error  string `json:"error"`
	Count  int    `json:"count"`
	Oldest string `json:"oldest"`
	Newest string `json:"newest"`
}

type auditDLQInspectResult struct {
	clispec.ResultMarker
	Command string                  `json:"command"`
	Rows    int                     `json:"rows"`
	ByError []auditDLQSummaryResult `json:"by_error"`
}

type auditDLQReplayResult struct {
	clispec.ResultMarker
	Command  string `json:"command"`
	DryRun   bool   `json:"dry_run"`
	Limit    int    `json:"limit"`
	Selected int    `json:"selected"`
	Replayed int    `json:"replayed"`
}

func auditDLQInspectOp(f *cli.Factory) clispec.Operation[auditDLQInspectInput] {
	return clispec.Operation[auditDLQInspectInput]{
		Name:    clispec.Name{Canonical: "inspect", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbOpsAuditDLQInspect), Reads: true},
		Group:   auditDLQGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Count the dead-letter rows by failure",
		Long: "Reads the dead-letter table through the ledger reader and groups its " +
			"rows by the failure text the consumer recorded, oldest and newest first seen.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() auditDLQInspectInput { return auditDLQInspectInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ auditDLQInspectInput, sink clispec.ResultSink) error {
			return runAuditDLQInspect(ctx, f, sink)
		},
	}
}

func auditDLQReplayOp(f *cli.Factory) clispec.Operation[auditDLQReplayInput] {
	return clispec.Operation[auditDLQReplayInput]{
		Name:    clispec.Name{Canonical: "replay", CLIOverride: ""},
		Audit:   audit.Spec{Verb: string(audit.VerbOpsAuditDLQReplay), Mutates: true},
		Group:   auditDLQGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Send dead-letter rows back through the audit topic",
		Long: "Re-publishes the oldest dead-letter rows to the audit topic, byte for " +
			"byte, so the consumer projects each one again. A row that lands is " +
			"deleted by the consumer; one that fails again keeps its row with the " +
			"attempt counted. Nothing is sent without --execute.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditDLQReplayInput]{
			clispec.IntParam("limit", "how many rows to send this run, oldest first", defaultDLQReplayLimit,
				func(input *auditDLQReplayInput, value int) { input.Limit = value }),
		},
		New: func() auditDLQReplayInput {
			return auditDLQReplayInput{InputMarker: clispec.InputMarker{}, Limit: defaultDLQReplayLimit}
		},
		DryRun: func(ctx context.Context, input auditDLQReplayInput, sink clispec.ResultSink) error {
			return runAuditDLQReplay(ctx, f, input, sink, false)
		},
		Run: func(ctx context.Context, input auditDLQReplayInput, sink clispec.ResultSink) error {
			return runAuditDLQReplay(ctx, f, input, sink, true)
		},
	}
}

func auditDLQReader(ctx context.Context, f *cli.Factory) (*audit.Reader, error) {
	if f == nil || f.Cfg == nil || strings.TrimSpace(f.Cfg.AuditReaderDSN) == "" {
		err := errors.New("the dead-letter commands need AUDIT_READER_DSN")
		slog.ErrorContext(ctx, "audit.dlq.reader_dsn_missing", slog.String("err", err.Error()))
		return nil, err
	}
	reader, err := audit.NewReader(ctx, f.Cfg.AuditReaderDSN)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.reader_open_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open the ledger reader for the dead-letter table: %w", err)
	}
	return reader, nil
}

func runAuditDLQInspect(ctx context.Context, f *cli.Factory, sink clispec.ResultSink) error {
	reader, err := auditDLQReader(ctx, f)
	if err != nil {
		return err
	}
	defer reader.Close()
	summaries, err := reader.SummarizeDeadLetters(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.inspect_failed", slog.String("err", err.Error()))
		return fmt.Errorf("inspect the dead-letter table: %w", err)
	}
	result := auditDLQInspectResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.audit.dlq.inspect", Rows: 0,
		ByError: make([]auditDLQSummaryResult, 0, len(summaries)),
	}
	for _, summary := range summaries {
		result.Rows += summary.Count
		result.ByError = append(result.ByError, auditDLQSummaryResult{
			Error: summary.Error, Count: summary.Count,
			Oldest: summary.Oldest.UTC().Format(time.RFC3339), Newest: summary.Newest.UTC().Format(time.RFC3339),
		})
	}
	if result.Rows > 0 {
		// A non-empty table means audit writes failed and nobody has replayed
		// them; the line is at error level so the log alarm sees it.
		slog.ErrorContext(ctx, "audit.dlq.non_empty",
			slog.Int("rows", result.Rows),
			slog.String("err", fmt.Sprintf("%d audit events are dead-lettered and not yet replayed", result.Rows)))
	}
	return writeAuditDLQResult(ctx, sink, result)
}

func runAuditDLQReplay(ctx context.Context, f *cli.Factory, input auditDLQReplayInput, sink clispec.ResultSink, execute bool) error {
	if input.Limit <= 0 {
		return errors.New("--limit must be at least 1")
	}
	reader, err := auditDLQReader(ctx, f)
	if err != nil {
		return err
	}
	defer reader.Close()
	letters, err := reader.ListDeadLetters(ctx, input.Limit)
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.list_for_replay_failed", slog.String("err", err.Error()))
		return fmt.Errorf("list the dead-letter rows to replay: %w", err)
	}
	result := auditDLQReplayResult{
		ResultMarker: clispec.ResultMarker{}, Command: "ops.audit.dlq.replay", DryRun: !execute,
		Limit: input.Limit, Selected: len(letters), Replayed: 0,
	}
	if !execute || len(letters) == 0 {
		return writeAuditDLQResult(ctx, sink, result)
	}
	producer, err := auditDLQProducer(ctx, f)
	if err != nil {
		return err
	}
	defer func() { _ = producer.CloseContext(ctx) }()
	for _, letter := range letters {
		if err := producer.Replay(ctx, letter); err != nil {
			slog.ErrorContext(ctx, "audit.dlq.replay_stopped",
				slog.Int("replayed", result.Replayed), slog.Int("selected", len(letters)),
				slog.String("err", err.Error()))
			return fmt.Errorf("replayed %d of %d dead letters: %w", result.Replayed, len(letters), err)
		}
		result.Replayed++
	}
	return writeAuditDLQResult(ctx, sink, result)
}

// auditDLQProducer opens the same producer the app records through, so a
// replay reaches the topic the consumer reads.
func auditDLQProducer(ctx context.Context, f *cli.Factory) (*audit.KafkaRecorder, error) {
	brokers := strings.TrimSpace(f.Cfg.AuditKafkaBrokers)
	if brokers == "" {
		err := errors.New("the dead-letter replay needs AUDIT_KAFKA_BROKERS")
		slog.ErrorContext(ctx, "audit.dlq.brokers_missing", slog.String("err", err.Error()))
		return nil, err
	}
	producer, err := audit.NewKafkaRecorder(audit.KafkaConfig{
		Brokers:        strings.Split(brokers, ","),
		Topic:          f.Cfg.AuditKafkaTopic,
		ClientID:       "tack-audit-dlq-replay",
		ProduceTimeout: f.Cfg.AuditKafkaProduceTimeout,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.dlq.producer_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("open the audit producer for the replay: %w", err)
	}
	return producer, nil
}

func writeAuditDLQResult(ctx context.Context, sink clispec.ResultSink, result clispec.Result) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "audit.dlq.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the dead-letter report: %w", err)
	}
	return nil
}
