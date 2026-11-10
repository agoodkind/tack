package ops

import (
	"time"

	"goodkind.io/tack/internal/clispec"
	"goodkind.io/tack/internal/datagen"
)

type datagenSearchLoadResult struct {
	clispec.ResultMarker
	Command             string                   `json:"command"`
	DryRun              bool                     `json:"dry_run"`
	Seed                int64                    `json:"seed"`
	TargetRatePerMinute int                      `json:"target_rate_per_minute"`
	Duration            string                   `json:"duration"`
	Concurrency         int                      `json:"concurrency"`
	Planned             int                      `json:"planned"`
	Sent                int                      `json:"sent"`
	Completed           int                      `json:"completed"`
	Dropped             int                      `json:"dropped"`
	DistinctQueryTexts  int                      `json:"distinct_query_texts"`
	CompletedPerMinute  float64                  `json:"completed_per_minute"`
	Elapsed             string                   `json:"elapsed"`
	Errors              datagenSearchLoadErrors  `json:"errors"`
	LatencyMs           datagenSearchLoadLatency `json:"latency_ms"`
	MaxWallTime         string                   `json:"max_wall_time"`
	StopReason          string                   `json:"stop_reason"`
}

type datagenSearchLoadErrors struct {
	Unavailable    int `json:"unavailable"`
	ToolError      int `json:"tool_error"`
	TransportError int `json:"transport_error"`
}

type datagenSearchLoadLatency struct {
	Min float64 `json:"min"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

// datagenSearchLoadReport adds the endpoint count and point-in-time report to the result
// fields that search-load and search-mixed share.
type datagenSearchLoadReport struct {
	datagenSearchLoadResult
	Endpoints   int                          `json:"endpoints"`
	PointInTime datagenSearchLoadPointInTime `json:"point_in_time"`
}

type datagenSearchLoadPointInTime struct {
	Peak     int                                  `json:"peak"`
	Answered int                                  `json:"answered_samples"`
	Samples  []datagenSearchLoadPointInTimeSample `json:"samples"`
}

type datagenSearchLoadPointInTimeSample struct {
	OffsetMs    float64 `json:"offset_ms"`
	Index       string  `json:"index,omitempty"`
	Open        int     `json:"open"`
	Unavailable bool    `json:"unavailable"`
	Status      int     `json:"status"`
	Error       string  `json:"error,omitempty"`
}

func searchLoadReport(
	input datagenSearchLoadInput, duration time.Duration, endpoints int, summary datagen.SearchLoadSummary, stop datagenSearchStop,
) datagenSearchLoadReport {
	samples := make([]datagenSearchLoadPointInTimeSample, 0, len(summary.PointInTime.Samples))
	for _, sample := range summary.PointInTime.Samples {
		samples = append(samples, datagenSearchLoadPointInTimeSample{
			OffsetMs: searchLoadMilliseconds(sample.Offset), Index: sample.Index, Open: sample.Open,
			Unavailable: sample.Unavailable, Status: sample.Status, Error: sample.Error,
		})
	}
	return datagenSearchLoadReport{
		datagenSearchLoadResult: datagenSearchLoadResult{
			ResultMarker: clispec.ResultMarker{}, Command: "ops.qa.datagen.search_load",
			DryRun: summary.DryRun, Seed: int64(input.Seed), TargetRatePerMinute: input.Rate,
			Duration: duration.String(), Concurrency: input.Concurrency, Planned: summary.Planned,
			Sent: summary.Sent, Completed: summary.Completed, Dropped: summary.Dropped,
			DistinctQueryTexts: summary.DistinctQueries, CompletedPerMinute: searchMixedPerMinute(summary.Completed, summary.Elapsed),
			Elapsed: summary.Elapsed.String(),
			Errors: datagenSearchLoadErrors{
				Unavailable: summary.Unavailable, ToolError: summary.ToolErrors, TransportError: summary.TransportErrors,
			},
			LatencyMs: datagenSearchLoadLatency{
				Min: searchLoadMilliseconds(summary.Latency.Min), P50: searchLoadMilliseconds(summary.Latency.P50),
				P95: searchLoadMilliseconds(summary.Latency.P95), P99: searchLoadMilliseconds(summary.Latency.P99),
				Max: searchLoadMilliseconds(summary.Latency.Max),
			},
			MaxWallTime: stop.maxWallTime.String(), StopReason: string(stop.reason),
		},
		Endpoints: endpoints,
		PointInTime: datagenSearchLoadPointInTime{
			Peak: summary.PointInTime.Peak, Answered: summary.PointInTime.Answered, Samples: samples,
		},
	}
}
