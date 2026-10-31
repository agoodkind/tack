package resourcesummary_test

import (
	"slices"
	"testing"

	"goodkind.io/tack/internal/datagen/resourcesummary"
)

const runLogFixture = "testdata/run.log"

// TestSummarizeRunFixture checks the node figures from a hand-written run log. The log
// has one-line slog records around the indented result and one sample without node readings.
// The second node-b reading has fewer heap bytes and a larger heap fraction than the first.
// node-c has one reading.
func TestSummarizeRunFixture(t *testing.T) {
	got, err := resourcesummary.SummarizeRun(t.Context(), runLogFixture)
	if err != nil {
		t.Fatalf("summarize %s: %v", runLogFixture, err)
	}

	want := resourcesummary.RunSummary{
		Samples: 3,
		Nodes: []resourcesummary.NodeSummary{
			{
				Name:                  "node-a",
				Readings:              2,
				CPUPercentMax:         90,
				ProcessCPUPercentMax:  80,
				HeapUsedMaxBytes:      1610612736,
				HeapUsedMaxFraction:   0.75,
				MemoryUsedMaxFraction: 0.875,
				DiskAvailableMinBytes: 21474836480,
				DiskTotalBytes:        53687091200,
			},
			{
				Name:                  "node-b",
				Readings:              2,
				CPUPercentMax:         95,
				ProcessCPUPercentMax:  10,
				HeapUsedMaxBytes:      1073741824,
				HeapUsedMaxFraction:   0.75,
				MemoryUsedMaxFraction: 0.75,
				DiskAvailableMinBytes: 64424509440,
				DiskTotalBytes:        107374182400,
			},
			{
				Name:                  "node-c",
				Readings:              1,
				CPUPercentMax:         30,
				ProcessCPUPercentMax:  20,
				HeapUsedMaxBytes:      268435456,
				HeapUsedMaxFraction:   0.25,
				MemoryUsedMaxFraction: 0.125,
				DiskAvailableMinBytes: 5368709120,
				DiskTotalBytes:        10737418240,
			},
		},
	}
	if got.Samples != want.Samples || !slices.Equal(got.Nodes, want.Nodes) {
		t.Errorf("summary = %+v, want %+v", got, want)
	}
}
