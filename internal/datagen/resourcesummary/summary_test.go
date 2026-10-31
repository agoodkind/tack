package resourcesummary_test

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/tack/internal/datagen/resourcesummary"
)

const (
	samplerDirectoryVariable = "TACK_RESOURCE_SAMPLER_DIR"
	runLogDirectoryVariable  = "TACK_RESOURCE_RUN_LOG_DIR"
	searchGuestID            = "227"
	// exactDecimals marks a value that the test compares without rounding.
	exactDecimals = -1
	decimalBase   = 10
)

// figure compares got with want after rounding got to decimals places, the precision of
// the posted TACK-544 tables.
type figure struct {
	name     string
	got      float64
	want     float64
	decimals int
}

// TestSavedRunResources summarizes the TACK-544 QA workload windows from saved files and
// checks every value in the posted tables.
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
			t.Logf("%s\n%s\n%s", run.name, hostTable(host, searchGuestID), nodeTable(workload))
			checkHost(t, run.host, host)
			checkWorkload(t, run, workload)
		})
	}
}

func checkHost(t *testing.T, want hostWant, got resourcesummary.HostSummary) {
	t.Helper()
	if !got.First.Equal(want.first) || !got.Last.Equal(want.last) {
		t.Errorf("first and last = %s and %s, earlier result %s and %s",
			got.First, got.Last, want.first, want.last)
	}
	checkFigures(t, []figure{
		{name: "samples", got: float64(got.Samples), want: float64(want.samples), decimals: exactDecimals},
		{name: "failed samples", got: float64(got.FailedSamples), want: float64(want.failedSamples), decimals: exactDecimals},
		{name: "MemAvailable min GiB", got: got.MemAvailableMinGiB, want: want.memAvailableMinGiB, decimals: 3},
		{name: "memory PSI some", got: got.MemoryPressure.SomeAvg10Max, want: want.memoryPressure.SomeAvg10Max, decimals: exactDecimals},
		{name: "memory PSI full", got: got.MemoryPressure.FullAvg10Max, want: want.memoryPressure.FullAvg10Max, decimals: exactDecimals},
		{name: "CPU PSI some", got: got.CPUPressure.SomeAvg10Max, want: want.cpuPressureSome, decimals: exactDecimals},
		{name: "1-minute load max", got: got.LoadOneMinuteMax, want: want.loadOneMinuteMax, decimals: exactDecimals},
		{name: "IO PSI some", got: got.IOPressure.SomeAvg10Max, want: want.ioPressure.SomeAvg10Max, decimals: exactDecimals},
		{name: "IO PSI full", got: got.IOPressure.FullAvg10Max, want: want.ioPressure.FullAvg10Max, decimals: exactDecimals},
		{name: "guest CPU cores", got: got.GuestCPUCores, want: want.guestCPUCores, decimals: 3},
		{name: "guest CPU PSI some", got: got.GuestCPUPressure.SomeAvg10Max, want: want.guestCPUPressure.SomeAvg10Max, decimals: exactDecimals},
		{name: "guest CPU PSI full", got: got.GuestCPUPressure.FullAvg10Max, want: want.guestCPUPressure.FullAvg10Max, decimals: exactDecimals},
		{name: "guest IO PSI some", got: got.GuestIOPressure.SomeAvg10Max, want: want.guestIOPressure.SomeAvg10Max, decimals: exactDecimals},
		{name: "guest IO PSI full", got: got.GuestIOPressure.FullAvg10Max, want: want.guestIOPressure.FullAvg10Max, decimals: exactDecimals},
		{name: "guest read MiB", got: got.GuestReadMiB, want: want.guestReadMiB, decimals: 1},
		{name: "guest write MiB", got: got.GuestWriteMiB, want: want.guestWriteMiB, decimals: exactDecimals},
	})
}

func checkWorkload(t *testing.T, run savedRun, got resourcesummary.RunSummary) {
	t.Helper()
	if got.Samples != run.workloadSamples || len(got.Nodes) != 1 || got.Nodes[0].Name != run.node.name {
		t.Fatalf("workload samples = %d with nodes %+v, earlier result %d with node %s",
			got.Samples, got.Nodes, run.workloadSamples, run.node.name)
	}
	node := got.Nodes[0]
	want := run.node
	checkFigures(t, []figure{
		{name: "readings", got: float64(node.Readings), want: float64(want.readings), decimals: exactDecimals},
		{name: "cpu_percent max", got: float64(node.CPUPercentMax), want: float64(want.cpuPercentMax), decimals: exactDecimals},
		{name: "process_cpu_percent max", got: float64(node.ProcessCPUPercentMax), want: float64(want.processCPUPercentMax), decimals: exactDecimals},
		{name: "heap_used_bytes max", got: float64(node.HeapUsedMaxBytes), want: float64(want.heapUsedMaxBytes), decimals: exactDecimals},
		{name: "heap fraction percent", got: node.HeapUsedMaxFraction * percentScale, want: want.heapUsedMaxPercent, decimals: 2},
		{name: "memory used fraction percent", got: node.MemoryUsedMaxFraction * percentScale, want: want.memoryUsedMaxPercent, decimals: 2},
		{name: "disk_available_bytes min", got: float64(node.DiskAvailableMinBytes), want: float64(want.diskAvailableMinBytes), decimals: exactDecimals},
		{name: "disk_total_bytes", got: float64(node.DiskTotalBytes), want: float64(want.diskTotalBytes), decimals: exactDecimals},
	})
}

func checkFigures(t *testing.T, figures []figure) {
	t.Helper()
	for _, item := range figures {
		got := item.got
		if item.decimals != exactDecimals {
			scale := math.Pow(decimalBase, float64(item.decimals))
			got = math.Round(got*scale) / scale
		}
		if got != item.want {
			t.Errorf("%s = %v, earlier result %v", item.name, got, item.want)
		}
	}
}
