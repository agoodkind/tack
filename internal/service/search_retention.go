package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/config"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/telemetry"
)

// RetentionRebuilds reads the current index replacement and begins a new
// one. RetiredSince returns the time when an index first retired a page.
type RetentionRebuilds interface {
	CurrentRebuild(context.Context) (searchdomain.Rebuild, bool, error)
	BeginRebuild(context.Context, searchdomain.BeginRebuild) (searchdomain.Rebuild, error)
	RetiredSince(context.Context, string) (time.Time, bool, error)
}

// SearchRetention starts a full FoundationDB replacement when the serving
// index exceeds its retired page count limit, its primary byte limit, or its
// oldest retirement age limit.
type SearchRetention struct {
	rebuilds RetentionRebuilds
	engine   searchdomain.RetentionReader
	index    searchdomain.ServingIndexReader
	clock    clock.Clock
	settings config.SearchRetentionSettings
	topology config.SearchTopology
}

// NewSearchRetention constructs the retention check. Every replacement it
// begins uses topology.
func NewSearchRetention(rebuilds RetentionRebuilds, engine searchdomain.RetentionReader, index searchdomain.ServingIndexReader, source clock.Clock, settings config.SearchRetentionSettings, topology config.SearchTopology) *SearchRetention {
	return &SearchRetention{rebuilds: rebuilds, engine: engine, index: index, clock: source, settings: settings, topology: topology}
}

// Interval returns the time between two checks.
func (r *SearchRetention) Interval() time.Duration { return r.settings.CheckInterval }

// Check compares the serving index with every threshold and begins one full
// replacement when the index exceeds any threshold. Check returns without a
// comparison while a replacement is in progress or when no serving index is
// recorded.
func (r *SearchRetention) Check(ctx context.Context) error {
	_, running, err := r.rebuilds.CurrentRebuild(ctx)
	if err != nil {
		return retentionFailure(ctx, "read index replacement", err)
	}
	if running {
		return nil
	}
	serving, err := r.index.ServingSearchIndex(ctx)
	if errors.Is(err, searchdomain.ErrNoServingIndex) {
		return nil
	}
	if err != nil {
		return retentionFailure(ctx, "read serving search index", err)
	}
	stats, err := r.engine.IndexRetention(ctx, serving)
	if err != nil {
		return retentionFailure(ctx, "read retention of index "+serving, err)
	}
	since, retired, err := r.rebuilds.RetiredSince(ctx, serving)
	if err != nil {
		return retentionFailure(ctx, "read retirement age of index "+serving, err)
	}
	reason := r.crossedThreshold(stats, since, retired)
	if reason == "" {
		return nil
	}
	rebuild, err := r.rebuilds.BeginRebuild(ctx, searchdomain.BeginRebuild{
		Mode: searchdomain.ReplacementFull, PrimaryShards: r.topology.Primaries, RoutingShards: r.topology.RoutingShards,
		Replicas: r.topology.Replicas, Restored: false, Reason: reason,
	})
	// Another process can begin a replacement between CurrentRebuild and
	// BeginRebuild. That replacement serves the same purpose, and this check
	// begins none.
	if errors.Is(err, searchdomain.ErrRebuildInProgress) {
		telemetry.L(ctx).InfoContext(ctx, "search.retention.replacement_running", slog.String("index", serving), slog.String("reason", reason))
		return nil
	}
	if err != nil {
		return retentionFailure(ctx, "begin retention replacement of index "+serving, err)
	}
	telemetry.L(ctx).InfoContext(ctx, "search.retention.replacement_started", slog.String("index", serving),
		slog.String("reason", reason), slog.String("rebuild_id", rebuild.ID.String()),
		slog.Int64("retired_pages", stats.RetiredPages), slog.Int64("primary_bytes", stats.PrimaryBytes))
	return nil
}

func (r *SearchRetention) crossedThreshold(stats searchdomain.RetentionStats, since time.Time, retired bool) string {
	switch {
	case stats.RetiredPages > r.settings.MaxRetiredPages:
		return fmt.Sprintf("retired pages %d exceed %d", stats.RetiredPages, r.settings.MaxRetiredPages)
	case stats.PrimaryBytes > r.settings.MaxIndexBytes:
		return fmt.Sprintf("primary bytes %d exceed %d", stats.PrimaryBytes, r.settings.MaxIndexBytes)
	case retired && r.clock.Since(since) > r.settings.MaxRetirementAge:
		return fmt.Sprintf("oldest retirement age exceeds %s", r.settings.MaxRetirementAge)
	}
	return ""
}

// retentionFailure logs one failed retention step at Error and returns it
// wrapped. The retention loop keeps no other record of the failure.
func retentionFailure(ctx context.Context, operation string, err error) error {
	wrapped := fmt.Errorf("search retention: %s: %w", operation, err)
	telemetry.L(ctx).ErrorContext(ctx, "search.retention.check_failed", slog.String("err", wrapped.Error()))
	return wrapped
}
