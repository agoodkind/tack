package resourcesummary_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/tack/internal/datagen/resourcesummary"
)

const (
	samplerDirectoryVariable = "TACK_RESOURCE_SAMPLER_DIR"
	runLogDirectoryVariable  = "TACK_RESOURCE_RUN_LOG_DIR"
	searchGuestID            = "227"
	gibibytesRoundingScale   = 1000
)

type savedRun struct {
	name                 string
	samplerFile          string
	runLogFile           string
	window               resourcesummary.Window
	wantMemoryPressure   resourcesummary.Pressure
	wantMemAvailableMinGiB float64
}

func savedRuns() []savedRun {
	return []savedRun{
		{
			name:        "rate 1000",
			samplerFile: "tack575-554f2181.jsonl",
			runLogFile:  "r1000-554f2181-run.log",
			window: resourcesummary.Window{
				Start: time.Date(2026, time.October, 30, 10, 21, 38, 0, time.UTC),
				End:   time.Date(2026, time.October, 30, 10, 42, 16, 0, time.UTC),
			},
			wantMemoryPressure:   resourcesummary.Pressure{SomeAvg10Max: 11.14, FullAvg10Max: 4.17},
			wantMemAvailableMinGiB: 4.874,
		},
		{
			name:        "rate 3000",
			samplerFile: "tack575-554f2181-r3000.jsonl",
			runLogFile:  "r3000-554f2181-run.log",
			window: resourcesummary.Window{
				Start: time.Date(2026, time.October, 30, 15, 5, 13, 0, time.UTC),
				End:   time.Date(2026, time.October, 30, 15, 29, 57, 0, time.UTC),
			},
			wantMemoryPressure:   resourcesummary.Pressure{SomeAvg10Max: 6.21, FullAvg10Max: 1.97},
			wantMemAvailableMinGiB: 4.577,
		},
	}
}

// TestSavedRunResources summarizes the TACK-544 QA workload windows from saved files.
func TestSavedRunResources(t *testing.T) {
	samplerDirectory := os.Getenv(samplerDirectoryVariable)
	runLogDirectory := os.Getenv(runLogDirectoryVariable)
	if samplerDirectory == "" || runLogDirectory == "" {
		t.Skipf("set %s and %s to summarize saved runs",
			samplerDirectoryVariable, runLogDirectoryVariable)
	}
	for _, run := range savedRuns() {
		t.Run(run.name, func(t *testing.T) {
			samplerPath := filepath.Join(samplerDirectory, run.samplerFile)
			host, err := resourcesummary.SummarizeHost(
				t.Context(), samplerPath, run.window, searchGuestID)
			if err != nil {
				t.Fatalf("summarize %s: %v", samplerPath, err)
			}
			runLogPath := filepath.Join(runLogDirectory, run.runLogFile)
			workload, err := resourcesummary.SummarizeRun(t.Context(), runLogPath)
			if err != nil {
				t.Fatalf("summarize %s: %v", runLogPath, err)
			}
			t.Logf("%s\n%s\n%s", run.name, hostTable(host), nodeTable(workload))
			checkMemory(t, run, host)
		})
	}
}

func checkMemory(t *testing.T, run savedRun, host resourcesummary.HostSummary) {
	t.Helper()
	if host.MemoryPressure != run.wantMemoryPressure {
		t.Errorf("memory pressure = %+v, earlier result %+v",
			host.MemoryPressure, run.wantMemoryPressure)
	}
	roundedGiB := math.Round(host.MemAvailableMinGiB*gibibytesRoundingScale) /
		gibibytesRoundingScale
	if roundedGiB != run.wantMemAvailableMinGiB {
		t.Errorf("MemAvailable minimum = %.3f GiB, earlier result %.3f GiB",
			roundedGiB, run.wantMemAvailableMinGiB)
	}
}
