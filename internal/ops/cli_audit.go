package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

func auditParityOp(f *cli.Factory) clispec.Operation[noInput] {
	return clispec.Operation[noInput]{
		Name:    clispec.Name{Canonical: "parity", CLIOverride: ""},
		Group:   auditOpsGroup,
		Aliases: nil,
		Hidden:  false,
		Short:   "Compare audit.events vs audit.events_v2 over a time window",
		Long: "Scan the dual-write parity window set by TACK_PARITY_FROM and " +
			"TACK_PARITY_TO (RFC3339 UTC), with an optional TACK_PARITY_THRESHOLD " +
			"matched-fraction floor in [0,1] (default 1.0). Exits non-zero when the " +
			"matched fraction falls below the threshold.",
		Examples: nil,
		Args:     nil,
		Params:   nil,
		New:      func() noInput { return noInput{InputMarker: clispec.InputMarker{}} },
		Run: func(ctx context.Context, _ noInput, sink clispec.ResultSink) error {
			from, to, threshold, err := readParityWindow()
			if err != nil {
				return err
			}
			dsn := f.Cfg.AuditReaderDSN
			if dsn == "" {
				dsn = f.Cfg.AuditWriterDSN
			}
			if dsn == "" {
				return errors.New("audit parity: AUDIT_READER_DSN or AUDIT_WRITER_DSN required")
			}
			pool, err := openParityPool(ctx, dsn)
			if err != nil {
				return err
			}
			defer pool.Close()
			result, err := runParityScan(ctx, pgxParityScanner{pool: pool}, from, to, threshold)
			if err != nil {
				return err
			}
			if err := clispec.WriteJSONValue(ctx, sink, result); err != nil {
				return fmt.Errorf("audit parity: emit: %w", err)
			}
			if result.Total > 0 && result.MatchedFraction < threshold {
				thresholdErr := fmt.Errorf(
					"audit parity below threshold: matched_fraction=%.6f threshold=%.6f",
					result.MatchedFraction, threshold,
				)
				slog.ErrorContext(ctx, "audit.parity_below_threshold",
					slog.Float64("matched_fraction", result.MatchedFraction),
					slog.Float64("threshold", threshold),
					slog.String("err", thresholdErr.Error()))
				return thresholdErr
			}
			return nil
		},
	}
}
