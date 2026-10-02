package integration

import (
	"runtime"
	"testing"

	"goodkind.io/tack/internal/testenv"
)

// throughputContainer is one container the throughput workload uses.
type throughputContainer struct {
	role string
	name string
}

// recordThroughputLimits logs the CPU and memory limits of the OpenSearch
// engine, the FoundationDB fixture, and the container that runs this test
// process and both Tack servers.
func recordThroughputLimits(t *testing.T, corpus processThroughputCorpus) []throughputContainer {
	t.Helper()
	engine := testenv.OpenSearchWithMemory(t, nativeSearchMemoryBytes)
	containers := []throughputContainer{
		{role: "opensearch", name: engine.Container},
		{role: "foundationdb", name: testenv.FoundationDBContainer(corpus.fixture.Config.FDBClusterFile)},
	}
	runner, err := testenv.RunnerContainer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if runner == "" {
		t.Logf("throughput resource limits role=runner host=true host_cpus=%d: the test process and both Tack servers run on the host", runtime.NumCPU())
	} else {
		containers = append(containers, throughputContainer{role: "runner", name: runner})
	}
	for _, container := range containers {
		limits, err := testenv.ContainerResourceLimits(t.Context(), container.name)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("throughput resource limits role=%s %s", container.role, limits)
	}
	return containers
}

// runMeasuredThroughputTrial runs one trial and logs each container's cgroup
// CPU usage and throttled time before and after it.
func runMeasuredThroughputTrial(t *testing.T, corpus processThroughputCorpus, servers []*actualSearchServer, ordinal int, containers []throughputContainer) processThroughputTrial {
	t.Helper()
	before := sampleThroughputCPU(t, containers)
	trial := runProcessThroughputTrial(t, corpus, servers, ordinal)
	after := sampleThroughputCPU(t, containers)
	for index, container := range containers {
		t.Logf("throughput trial=%d processes=%d role=%s units=%d elapsed_seconds=%.6f usage_usec_before=%d usage_usec_after=%d throttled_usec_before=%d throttled_usec_after=%d nr_throttled_before=%d nr_throttled_after=%d cpu_seconds=%.3f",
			ordinal, len(servers), container.role, trial.sessionsCompleted+trial.mutationsIndexed, trial.seconds,
			before[index].UsageMicros, after[index].UsageMicros, before[index].ThrottledMicros, after[index].ThrottledMicros,
			before[index].ThrottledPeriods, after[index].ThrottledPeriods,
			float64(after[index].UsageMicros-before[index].UsageMicros)/1e6)
	}
	return trial
}

func sampleThroughputCPU(t *testing.T, containers []throughputContainer) []testenv.ContainerCPUSample {
	t.Helper()
	samples := make([]testenv.ContainerCPUSample, 0, len(containers))
	for _, container := range containers {
		sample, err := testenv.ContainerCPU(t.Context(), container.name)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, sample)
	}
	return samples
}
