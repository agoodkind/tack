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

// The dead-letter table stores audit events the consumer could not commit.
// These commands inspect failures and republish events to the audit topic.
// Only the consumer writes ledger events and deletes their dead-letter rows
// (TACK-336, TACK-240).

var auditDLQGroup = &clispec.Group{
	Use:    "dlq",
	Short:  "Inspect and replay the audit dead-letter table",
	Long:   "",
	Parent: auditOpsGroup,
}

// defaultDLQReplayLimit limits one run to permit reviewing consumer results
// before replaying further rows.
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
		Name:     clispec.Name{Canonical: "inspect", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    readSpec(audit.VerbOpsAuditDLQInspect, "audit/dlq/summary"),
		Group:    auditDLQGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Count the dead-letter rows by failure",
		Long: "Read the dead-letter table using the ledger reader. Group rows by " +
			"the consumer's recorded failure text and report the oldest and newest occurrence.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New: func() auditDLQInspectInput {
			return auditDLQInspectInput{
				InputMarker: clispec.InputMarker{},
			}
		},
		Run: func(ctx context.Context, _ auditDLQInspectInput, sink clispec.ResultSink) error {
			return runAuditDLQInspect(ctx, f, sink)
		},
	}
}

func auditDLQReplayOp(f *cli.Factory) clispec.Operation[auditDLQReplayInput] {
	return clispec.Operation[auditDLQReplayInput]{
		Name:     clispec.Name{Canonical: "replay", CLIOverride: ""},
		Lifetime: clispec.Permanent,
		Audit:    audit.Spec{Verb: string(audit.VerbOpsAuditDLQReplay), Mutates: true},
		Group:    auditDLQGroup,
		Aliases:  nil,
		Hidden:   false,
		Short:    "Republish dead-letter rows to the audit topic",
		Long: "Republish dead-letter rows to the audit topic, byte for byte, " +
			"for the consumer to retry. Select rows with the fewest attempts first " +
			"and the oldest rows first among equal attempt counts. This order " +
			"prevents repeated failures from delaying other rows. The consumer " +
			"deletes each row after a successful commit. After another failure, " +
			"it retains the row and increments its attempt count. " +
			"The command requires --execute to publish rows.",
		Examples: nil,
		Args:     nil,
		Params: []clispec.Param[auditDLQReplayInput]{
			clispec.IntParam(
				"limit",
				"how many rows to send this run, fewest attempts then oldest first",
				defaultDLQReplayLimit,
				func(input *auditDLQReplayInput, value int) { input.Limit = value },
			),
		},
		New: func() auditDLQReplayInput {
			return auditDLQReplayInput{
				InputMarker: clispec.InputMarker{},
				Limit:       defaultDLQReplayLimit,
			}
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

		return nil, fmt.Errorf(
			"open the ledger reader for the dead-letter table from AUDIT_READER_DSN: %w",
			err,
		)
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
		ResultMarker: clispec.ResultMarker{},
		Command:      "ops.audit.dlq.inspect",
		Rows:         0,
		ByError:      make([]auditDLQSummaryResult, 0, len(summaries)),
	}
	for _, summary := range summaries {
		result.Rows += summary.Count
		result.ByError = append(
			result.ByError,
			auditDLQSummaryResult{
				Error:  summary.Error,
				Count:  summary.Count,
				Oldest: summary.Oldest.UTC().Format(time.RFC3339),
				Newest: summary.Newest.UTC().Format(time.RFC3339),
			},
		)
	}

	if result.Rows > 0 {
		// Failed audit writes remain in the table until replay succeeds.
		// Error-level logging triggers the log alarm.
		slog.ErrorContext(
			ctx,
			"audit.dlq.non_empty",
			slog.Int("rows", result.Rows),
			slog.String(
				"err",
				fmt.Sprintf("%d audit events await replay after failed ledger writes", result.Rows),
			),
		)
	}

	return writeAuditDLQResult(ctx, sink, result)
}

func writeAuditDLQResult(
	ctx context.Context,
	sink clispec.ResultSink,
	result clispec.Result,
) error {
	if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
		slog.ErrorContext(ctx, "audit.dlq.report_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write the dead-letter report: %w", err)
	}

	return nil
}
