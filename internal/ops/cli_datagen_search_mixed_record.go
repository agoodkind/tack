package ops

import (
	"context"
	"log/slog"

	"goodkind.io/tack/internal/datagen"
	"goodkind.io/tack/internal/telemetry"
)

// searchMixedResultRecord is the name of the log record that repeats the search-mixed result
// without the samples. The log file keeps this record after the command output is lost.
const searchMixedResultRecord = "qa.datagen.search_mixed_result"

func logSearchMixedResult(ctx context.Context, result datagenSearchMixedResult) {
	attributes := append(searchMixedLoadAttrs(result.datagenSearchLoadResult),
		slog.Int("target_write_rate_per_minute", result.TargetWriteRatePerMinute),
		slog.Int("planned_writes", result.PlannedWrites),
		slog.Group("writes", slog.Int("created", result.Writes.Created), slog.Int("edited", result.Writes.Edited),
			slog.Int("deleted", result.Writes.Deleted), slog.Int("metadata_changes", result.Writes.MetadataChanges),
			slog.Int("moves", result.Writes.Moves), slog.Int("failed", result.Writes.Failed)),
		slog.Float64("writes_per_minute", result.WritesPerMinute),
		slog.Group("freshness_lag_ms", slog.Float64("p50", result.FreshnessLagMs.P50),
			slog.Float64("p95", result.FreshnessLagMs.P95), slog.Float64("p99", result.FreshnessLagMs.P99),
			slog.Float64("max", result.FreshnessLagMs.Max)),
		slog.Int("markers_not_found", result.MarkersNotFound),
		slog.Int("deleted_nodes_returned", result.DeletedNodesReturned),
		slog.Int("deletes_unverified", result.DeletesUnverified),
		slog.Int("metadata_not_found", result.MetadataNotFound),
		slog.Int("moves_unverified", result.MovesUnverified),
		slog.Int("probe_errors", result.ProbeErrors),
		slog.Int("endpoints", result.Endpoints),
		slog.Int("tenants", result.Tenants),
		searchMixedBurstAttr(result.Burst),
		searchMixedSessionsAttr(result.Sessions),
		slog.Group("pending_deletion", slog.Int("checks", result.PendingDeletion.Checks),
			slog.Int("pending_observed", result.PendingDeletion.PendingObserved),
			slog.Int("unavailable", result.PendingDeletion.Unavailable),
			slog.Int("deleted_nodes_returned", result.PendingDeletion.DeletedNodesReturned)),
		searchMixedRevocationAttr(result.Revocation),
		slog.Group("rebuild", slog.Bool("requested", result.Rebuild.Requested),
			slog.String("target_index", result.Rebuild.TargetIndex), slog.Float64("started_ms", result.Rebuild.StartedMs),
			slog.Float64("switched_after_ms", result.Rebuild.SwitchedAfterMs),
			slog.String("final_state", result.Rebuild.FinalState)),
		slog.Group("catch_up", slog.Bool("completed", result.CatchUp.Completed),
			slog.Float64("elapsed_ms", result.CatchUp.ElapsedMs)),
		slog.Any("skipped_phases", result.SkippedPhases),
		slog.Group("corpus_index", slog.String("threshold", result.CorpusIndex.Threshold),
			slog.Any("waits", result.CorpusIndex.Waits)),
	)
	telemetry.L(ctx).LogAttrs(ctx, slog.LevelInfo, searchMixedResultRecord, attributes...)
}

func searchMixedLoadAttrs(load datagenSearchLoadResult) []slog.Attr {
	return []slog.Attr{
		slog.String("command", load.Command), slog.Bool("dry_run", load.DryRun), slog.Int64("seed", load.Seed),
		slog.Int("target_rate_per_minute", load.TargetRatePerMinute), slog.String("duration", load.Duration),
		slog.Int("concurrency", load.Concurrency), slog.Int("planned", load.Planned), slog.Int("sent", load.Sent),
		slog.Int("completed", load.Completed), slog.Int("dropped", load.Dropped),
		slog.Int("distinct_query_texts", load.DistinctQueryTexts),
		slog.Float64("completed_per_minute", load.CompletedPerMinute), slog.String("elapsed", load.Elapsed),
		slog.Group("errors", slog.Int("unavailable", load.Errors.Unavailable),
			slog.Int("tool_error", load.Errors.ToolError), slog.Int("transport_error", load.Errors.TransportError)),
		slog.Group("latency_ms", slog.Float64("min", load.LatencyMs.Min), slog.Float64("p50", load.LatencyMs.P50),
			slog.Float64("p95", load.LatencyMs.P95), slog.Float64("p99", load.LatencyMs.P99),
			slog.Float64("max", load.LatencyMs.Max)),
		slog.String("max_wall_time", load.MaxWallTime), slog.String("stop_reason", load.StopReason),
	}
}

func searchMixedBurstAttr(burst datagenSearchMixedBurst) slog.Attr {
	return slog.Group("burst", slog.Int("planned", burst.Planned), slog.Int("created", burst.Created),
		slog.Int("failed", burst.Failed), slog.Float64("started_ms", burst.StartedMs),
		slog.Float64("elapsed_ms", burst.ElapsedMs), slog.Float64("writes_per_minute", burst.WritesPerMinute))
}

func searchMixedSessionsAttr(sessions datagen.SearchMixedSessions) slog.Attr {
	return slog.Group("sessions", slog.Int("checks", sessions.Checks), slog.Int("pages", sessions.Pages),
		slog.Int("replays", sessions.Replays), slog.Int("endpoints", sessions.Endpoints),
		slog.Int("unavailable", sessions.Unavailable), slog.Int("expired_during_outage", sessions.ExpiredDuringOutage),
		slog.Int("continuation_errors", sessions.ContinuationErrors),
		slog.Int("missing_corpus_issues", sessions.MissingCorpusIssues),
		slog.Int("replay_mismatches", sessions.ReplayMismatches),
		slog.Int("cross_tenant_results", sessions.CrossTenantResults),
		slog.Int("deleted_nodes_returned", sessions.DeletedNodesReturned))
}

func searchMixedRevocationAttr(revocation datagenSearchMixedRevocation) slog.Attr {
	return slog.Group("revocation", slog.Int("checks", revocation.Checks),
		slog.Int("pages_within_cache_lifetime", revocation.PagesWithinCacheLifetime),
		slog.Int("results_after_cache_lifetime", revocation.ResultsAfterCacheLifetime),
		slog.Int("unavailable", revocation.Unavailable), slog.Bool("refused", revocation.Refused),
		slog.Float64("refused_after_ms", revocation.RefusedAfterMs))
}
