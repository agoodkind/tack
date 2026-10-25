package ops

import (
	"time"

	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
)

type datagenSearchMixedResult struct {
	datagenSearchLoadResult
	TargetWriteRatePerMinute int                          `json:"target_write_rate_per_minute"`
	PlannedWrites            int                          `json:"planned_writes"`
	Writes                   datagenSearchMixedWrites     `json:"writes"`
	WritesPerMinute          float64                      `json:"writes_per_minute"`
	FreshnessLagMs           datagenSearchMixedFreshness  `json:"freshness_lag_ms"`
	MarkersNotFound          int                          `json:"markers_not_found"`
	DeletedNodesReturned     int                          `json:"deleted_nodes_returned"`
	DeletesUnverified        int                          `json:"deletes_unverified"`
	ProbeErrors              int                          `json:"probe_errors"`
	Endpoints                int                          `json:"endpoints"`
	Tenants                  int                          `json:"tenants"`
	Burst                    datagenSearchMixedBurst      `json:"burst"`
	Sessions                 datagen.SearchMixedSessions  `json:"sessions"`
	PendingDeletion          datagen.SearchMixedPending   `json:"pending_deletion"`
	Revocation               datagenSearchMixedRevocation `json:"revocation"`
	Rebuild                  datagenSearchMixedRebuild    `json:"rebuild"`
	CatchUp                  datagenSearchMixedCatchUp    `json:"catch_up"`
	Samples                  []datagenSearchMixedSample   `json:"samples"`
	SkippedPhases            []string                     `json:"skipped_phases"`
}

type datagenSearchMixedWrites struct {
	Created int `json:"created"`
	Edited  int `json:"edited"`
	Deleted int `json:"deleted"`
	Failed  int `json:"failed"`
}

type datagenSearchMixedFreshness struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

type datagenSearchMixedBurst struct {
	Planned         int     `json:"planned"`
	Created         int     `json:"created"`
	Failed          int     `json:"failed"`
	StartedMs       float64 `json:"started_ms"`
	ElapsedMs       float64 `json:"elapsed_ms"`
	WritesPerMinute float64 `json:"writes_per_minute"`
}

type datagenSearchMixedRevocation struct {
	Checks                    int     `json:"checks"`
	PagesWithinCacheLifetime  int     `json:"pages_within_cache_lifetime"`
	ResultsAfterCacheLifetime int     `json:"results_after_cache_lifetime"`
	Refused                   bool    `json:"refused"`
	RefusedAfterMs            float64 `json:"refused_after_ms"`
}

type datagenSearchMixedRebuild struct {
	Requested       bool    `json:"requested"`
	TargetIndex     string  `json:"target_index"`
	StartedMs       float64 `json:"started_ms"`
	SwitchedAfterMs float64 `json:"switched_after_ms"`
	FinalState      string  `json:"final_state"`
}

type datagenSearchMixedCatchUp struct {
	Completed bool    `json:"completed"`
	ElapsedMs float64 `json:"elapsed_ms"`
}

type datagenSearchMixedSample struct {
	OffsetMs            float64                            `json:"offset_ms"`
	OldestPendingWorkMs float64                            `json:"oldest_pending_work_ms"`
	OldestClass         string                             `json:"oldest_class"`
	RebuildState        string                             `json:"rebuild_state"`
	OpenSearchNodes     []datagen.SearchMixedNodeResources `json:"opensearch_nodes"`
	Error               string                             `json:"error,omitempty"`
}

func searchMixedPerMinute(count int, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(count) / elapsed.Minutes()
}

func searchMixedResult(
	input datagenSearchMixedInput, duration time.Duration, summary datagen.SearchMixedSummary, stop datagenSearchStop,
) datagenSearchMixedResult {
	searches := summary.Search
	return datagenSearchMixedResult{
		datagenSearchLoadResult: datagenSearchLoadResult{
			ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.search_mixed",
			DryRun: searches.DryRun, Seed: int64(input.Seed), TargetRatePerMinute: input.Rate,
			Duration: duration.String(), Concurrency: input.Concurrency, Planned: searches.Planned,
			Sent: searches.Sent, Completed: searches.Completed, Dropped: searches.Dropped,
			DistinctQueryTexts: searches.DistinctQueries, CompletedPerMinute: searchMixedPerMinute(searches.Completed, searches.Elapsed),
			Elapsed: searches.Elapsed.String(),
			Errors: datagenSearchLoadErrors{
				Unavailable: searches.Unavailable, ToolError: searches.ToolErrors, TransportError: searches.TransportErrors,
			},
			LatencyMs: datagenSearchLoadLatency{
				Min: searchLoadMilliseconds(searches.Latency.Min), P50: searchLoadMilliseconds(searches.Latency.P50),
				P95: searchLoadMilliseconds(searches.Latency.P95), P99: searchLoadMilliseconds(searches.Latency.P99),
				Max: searchLoadMilliseconds(searches.Latency.Max),
			},
			MaxWallTime: stop.maxWallTime.String(), StopReason: string(stop.reason),
		},
		TargetWriteRatePerMinute: input.WriteRate, PlannedWrites: summary.PlannedWrites,
		Writes: datagenSearchMixedWrites{
			Created: summary.Creates, Edited: summary.Edits, Deleted: summary.Deletes, Failed: summary.WriteErrors,
		},
		WritesPerMinute: searchMixedPerMinute(summary.Creates+summary.Edits+summary.Deletes, summary.WriteElapsed),
		FreshnessLagMs: datagenSearchMixedFreshness{
			P50: searchLoadMilliseconds(summary.Freshness.P50), P95: searchLoadMilliseconds(summary.Freshness.P95),
			P99: searchLoadMilliseconds(summary.Freshness.P99), Max: searchLoadMilliseconds(summary.Freshness.Max),
		},
		MarkersNotFound: summary.MarkersNotFound, DeletedNodesReturned: summary.DeletedReturned,
		DeletesUnverified: summary.DeletesUnverified, ProbeErrors: summary.ProbeErrors,
		Endpoints: summary.Endpoints, Tenants: summary.Tenants,
		Burst: searchMixedBurstResult(summary.Burst), Sessions: summary.Sessions, PendingDeletion: summary.Pending,
		Revocation: datagenSearchMixedRevocation{
			Checks: summary.Revocation.Checks, PagesWithinCacheLifetime: summary.Revocation.PagesWithinCacheLifetime,
			ResultsAfterCacheLifetime: summary.Revocation.ResultsAfterCacheLifetime, Refused: summary.Revocation.Refused,
			RefusedAfterMs: searchLoadMilliseconds(summary.Revocation.RefusedAfter),
		},
		Rebuild: datagenSearchMixedRebuild{
			Requested: summary.Rebuild.Requested, TargetIndex: summary.Rebuild.TargetIndex,
			StartedMs: searchLoadMilliseconds(summary.Rebuild.Started), SwitchedAfterMs: searchLoadMilliseconds(summary.Rebuild.Switched),
			FinalState: summary.Rebuild.FinalState,
		},
		CatchUp: datagenSearchMixedCatchUp{Completed: summary.CatchUp.Completed, ElapsedMs: searchLoadMilliseconds(summary.CatchUp.Elapsed)},
		Samples:       searchMixedSamples(summary.Samples),
		SkippedPhases: append([]string{}, summary.SkippedPhases...),
	}
}

func searchMixedBurstResult(burst datagen.SearchMixedBurst) datagenSearchMixedBurst {
	return datagenSearchMixedBurst{
		Planned: burst.Planned, Created: burst.Created, Failed: burst.Failed,
		StartedMs: searchLoadMilliseconds(burst.Started), ElapsedMs: searchLoadMilliseconds(burst.Elapsed),
		WritesPerMinute: searchMixedPerMinute(burst.Created, burst.Elapsed),
	}
}

func searchMixedSamples(samples []datagen.SearchMixedSample) []datagenSearchMixedSample {
	results := make([]datagenSearchMixedSample, 0, len(samples))
	for _, sample := range samples {
		results = append(results, datagenSearchMixedSample{
			OffsetMs: searchLoadMilliseconds(sample.Offset), OldestPendingWorkMs: searchLoadMilliseconds(sample.OldestPendingWork),
			OldestClass: sample.OldestClass, RebuildState: sample.RebuildState, OpenSearchNodes: sample.Nodes, Error: sample.Error,
		})
	}
	return results
}
