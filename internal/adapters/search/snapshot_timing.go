package search

import (
	"context"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// snapshotTiming stores each step duration for one QueryRanker.Open call.
type snapshotTiming struct {
	started     time.Time
	previous    time.Time
	alias       time.Duration
	mapping     time.Duration
	model       time.Duration
	predict     time.Duration
	pointInTime time.Duration
}

// newSnapshotTiming starts timing one QueryRanker.Open call.
func newSnapshotTiming() *snapshotTiming {
	now := clock.Now()

	return &snapshotTiming{
		started:     now,
		previous:    now,
		alias:       0,
		mapping:     0,
		model:       0,
		predict:     0,
		pointInTime: 0,
	}
}

// mark stores the time since the previous mark in step.
func (t *snapshotTiming) mark(step *time.Duration) {
	now := clock.Now()
	*step = now.Sub(t.previous)
	t.previous = now
}

// log writes one Debug record with step durations in milliseconds.
// The record does not contain the query text.
func (t *snapshotTiming) log(ctx context.Context, index string) {
	telemetry.L(ctx).DebugContext(
		ctx,
		"search.snapshot.timing",
		slog.String("index", index),
		slog.Int64("alias_ms", t.alias.Milliseconds()),
		slog.Int64("mapping_ms", t.mapping.Milliseconds()),
		slog.Int64("model_ms", t.model.Milliseconds()),
		slog.Int64("predict_ms", t.predict.Milliseconds()),
		slog.Int64("point_in_time_ms", t.pointInTime.Milliseconds()),
		slog.Int64("total_ms", clock.Since(t.started).Milliseconds()),
	)
}
