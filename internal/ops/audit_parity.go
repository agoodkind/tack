// audit_parity.go: dual-write parity scan between audit.events (legacy) and
// audit.events_v2 (Wave 1 sibling). Reports per-classification counts and up
// to ten content-diff examples for a configurable event_time window. Exits
// non-zero when matched fraction falls below the threshold so the operator
// can wire it into a deploy gate.

package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/config"
)

const (
	parityFromEnv      = "TACK_PARITY_FROM"
	parityToEnv        = "TACK_PARITY_TO"
	parityThresholdEnv = "TACK_PARITY_THRESHOLD"
	parityExampleLimit = 10
)

type parityCounts struct {
	Matched     int64 `json:"matched"`
	OnlyLegacy  int64 `json:"only_legacy"`
	OnlyV2      int64 `json:"only_v2"`
	ContentDiff int64 `json:"content_diff"`
}

type parityFieldDiff struct {
	Field  string `json:"field"`
	Legacy string `json:"legacy"`
	V2     string `json:"v2"`
}

type parityExample struct {
	EventID uuid.UUID         `json:"event_id"`
	Diffs   []parityFieldDiff `json:"diffs"`
}

type parityResult struct {
	From            time.Time       `json:"from"`
	To              time.Time       `json:"to"`
	Threshold       float64         `json:"threshold"`
	Counts          parityCounts    `json:"counts"`
	Total           int64           `json:"total"`
	MatchedFraction float64         `json:"matched_fraction"`
	Examples        []parityExample `json:"content_diff_examples"`
}

// parityScanner is the minimum surface runParityScan needs. Real callers wire
// pgxParityScanner; tests substitute a fake to exercise drift categories
// without a live database.
type parityScanner interface {
	ScanCounts(ctx context.Context, from time.Time, to time.Time) (*parityCounts, error)
	ScanExamples(ctx context.Context, from time.Time, to time.Time, limit int) ([]parityExample, error)
}

func runAuditParity(ctx context.Context, cfg *config.Config) error {
	from, to, threshold, err := readParityWindow()
	if err != nil {
		return err
	}
	dsn := cfg.AuditReaderDSN
	if dsn == "" {
		dsn = cfg.AuditWriterDSN
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
	if encErr := writeParityResult(result); encErr != nil {
		return encErr
	}
	if result.Total > 0 && result.MatchedFraction < threshold {
		return fmt.Errorf(
			"audit parity below threshold: matched_fraction=%.6f threshold=%.6f",
			result.MatchedFraction, threshold,
		)
	}
	return nil
}

func runParityScan(ctx context.Context, scanner parityScanner, from time.Time, to time.Time, threshold float64) (*parityResult, error) {
	counts, err := scanner.ScanCounts(ctx, from, to)
	if err != nil {
		slog.ErrorContext(ctx, "audit.parity.scan_counts_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("scan parity counts: %w", err)
	}
	var examples []parityExample
	if counts.ContentDiff > 0 {
		examples, err = scanner.ScanExamples(ctx, from, to, parityExampleLimit)
		if err != nil {
			slog.ErrorContext(ctx, "audit.parity.scan_examples_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("scan parity examples: %w", err)
		}
	}
	if examples == nil {
		examples = []parityExample{}
	}
	total := counts.Matched + counts.OnlyLegacy + counts.OnlyV2 + counts.ContentDiff
	fraction := 1.0
	if total > 0 {
		fraction = float64(counts.Matched) / float64(total)
	}
	return &parityResult{
		From:            from,
		To:              to,
		Threshold:       threshold,
		Counts:          *counts,
		Total:           total,
		MatchedFraction: fraction,
		Examples:        examples,
	}, nil
}

func readParityWindow() (time.Time, time.Time, float64, error) {
	fromStr, err := readRequiredStringEnv(parityFromEnv)
	if err != nil {
		slog.Error("audit.parity.from_unset", slog.String("err", err.Error()))
		return time.Time{}, time.Time{}, 0, err
	}
	toStr, err := readRequiredStringEnv(parityToEnv)
	if err != nil {
		slog.Error("audit.parity.to_unset", slog.String("err", err.Error()))
		return time.Time{}, time.Time{}, 0, err
	}
	from, err := time.Parse(time.RFC3339, strings.TrimSpace(fromStr))
	if err != nil {
		slog.Error("audit.parity.from_parse_failed",
			slog.String("value", fromStr), slog.String("err", err.Error()))
		return time.Time{}, time.Time{}, 0, fmt.Errorf("parse %s %q: %w", parityFromEnv, fromStr, err)
	}
	to, err := time.Parse(time.RFC3339, strings.TrimSpace(toStr))
	if err != nil {
		slog.Error("audit.parity.to_parse_failed",
			slog.String("value", toStr), slog.String("err", err.Error()))
		return time.Time{}, time.Time{}, 0, fmt.Errorf("parse %s %q: %w", parityToEnv, toStr, err)
	}
	if !to.After(from) {
		err := fmt.Errorf("%s must be after %s", parityToEnv, parityFromEnv)
		slog.Error("audit.parity.window_inverted",
			slog.String("from", fromStr), slog.String("to", toStr),
			slog.String("err", err.Error()))
		return time.Time{}, time.Time{}, 0, err
	}
	threshold, err := readParityThreshold()
	if err != nil {
		return time.Time{}, time.Time{}, 0, err
	}
	return from.UTC(), to.UTC(), threshold, nil
}

func readParityThreshold() (float64, error) {
	raw, ok := os.LookupEnv(parityThresholdEnv)
	if !ok || strings.TrimSpace(raw) == "" {
		return 1.0, nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		slog.Error("audit.parity.threshold_parse_failed",
			slog.String("value", raw), slog.String("err", err.Error()))
		return 0, fmt.Errorf("parse %s %q: %w", parityThresholdEnv, raw, err)
	}
	if v < 0 || v > 1 {
		err := fmt.Errorf("%s must be in [0, 1], got %v", parityThresholdEnv, v)
		slog.Error("audit.parity.threshold_out_of_range",
			slog.Float64("value", v), slog.String("err", err.Error()))
		return 0, err
	}
	return v, nil
}

func writeParityResult(result *parityResult) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		slog.Error("audit.parity.encode_failed", slog.String("err", err.Error()))
		return fmt.Errorf("encode parity result: %w", err)
	}
	return nil
}
