package resourcesummary_test

import (
	"testing"
	"time"

	"goodkind.io/tack/internal/datagen/resourcesummary"
)

const samplerFixture = "testdata/sampler.jsonl"

// TestSummarizeHostFixture checks every host and guest figure on hand-written sampler lines.
// The fixture has one line before and one line after the window, with readings of 99 that a
// window error would expose, and one failed line inside the window.
func TestSummarizeHostFixture(t *testing.T) {
	windowStart := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	window := resourcesummary.Window{Start: windowStart, End: windowStart.Add(time.Minute)}

	got, err := resourcesummary.SummarizeHost(t.Context(), samplerFixture, window, searchGuestID)
	if err != nil {
		t.Fatalf("summarize %s: %v", samplerFixture, err)
	}

	if got.Samples != 3 || got.FailedSamples != 1 {
		t.Errorf("samples = %d, failed = %d, want 3 and 1", got.Samples, got.FailedSamples)
	}
	if !got.First.Equal(windowStart) || got.Elapsed() != time.Minute {
		t.Errorf("first = %s, elapsed = %s, want %s and 1m0s", got.First, got.Elapsed(), windowStart)
	}
	figures := []struct {
		name string
		got  float64
		want float64
	}{
		{name: "MemAvailableMinGiB", got: got.MemAvailableMinGiB, want: 2},
		{name: "MemoryPressure some", got: got.MemoryPressure.SomeAvg10Max, want: 4.25},
		{name: "MemoryPressure full", got: got.MemoryPressure.FullAvg10Max, want: 2.75},
		{name: "CPUPressure some", got: got.CPUPressure.SomeAvg10Max, want: 30.5},
		{name: "CPUPressure full", got: got.CPUPressure.FullAvg10Max, want: 0},
		{name: "IOPressure some", got: got.IOPressure.SomeAvg10Max, want: 7},
		{name: "IOPressure full", got: got.IOPressure.FullAvg10Max, want: 3.25},
		{name: "LoadOneMinuteMax", got: got.LoadOneMinuteMax, want: 8.5},
		{name: "GuestCPUCores", got: got.GuestCPUCores, want: 0.5},
		{name: "GuestCPUPressure some", got: got.GuestCPUPressure.SomeAvg10Max, want: 40},
		{name: "GuestCPUPressure full", got: got.GuestCPUPressure.FullAvg10Max, want: 5},
		{name: "GuestIOPressure some", got: got.GuestIOPressure.SomeAvg10Max, want: 9.5},
		{name: "GuestIOPressure full", got: got.GuestIOPressure.FullAvg10Max, want: 6},
		{name: "GuestReadMiB", got: got.GuestReadMiB, want: 6},
		{name: "GuestWriteMiB", got: got.GuestWriteMiB, want: 3},
	}
	for _, figure := range figures {
		if figure.got != figure.want {
			t.Errorf("%s = %v, want %v", figure.name, figure.got, figure.want)
		}
	}
}
