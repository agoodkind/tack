package resourcesummary

import (
	"errors"
	"time"
)

const minimumSamples = 2

type hostAccumulator struct {
	summary     HostSummary
	firstUsage  int64
	lastUsage   int64
	firstIOStat ioBytes
	lastIOStat  ioBytes
}

func newHostAccumulator() *hostAccumulator {
	return &hostAccumulator{
		summary:     HostSummary{},
		firstUsage:  0,
		lastUsage:   0,
		firstIOStat: ioBytes{read: 0, written: 0},
		lastIOStat:  ioBytes{read: 0, written: 0},
	}
}

func (accumulator *hostAccumulator) add(sample hostSample) {
	summary := &accumulator.summary
	if summary.Samples == 0 {
		summary.First = sample.time
		summary.MemAvailableMinGiB = sample.memAvailableGiB
		accumulator.firstUsage = sample.guestUsageMicroseconds
		accumulator.firstIOStat = sample.guestIOStat
	}
	summary.Samples++
	summary.Last = sample.time
	summary.MemAvailableMinGiB = min(summary.MemAvailableMinGiB, sample.memAvailableGiB)
	summary.LoadOneMinuteMax = max(summary.LoadOneMinuteMax, sample.loadOneMinute)
	summary.MemoryPressure.include(sample.memoryPressure)
	summary.CPUPressure.include(sample.cpuPressure)
	summary.IOPressure.include(sample.ioPressure)
	summary.GuestCPUPressure.include(sample.guestCPUPressure)
	summary.GuestIOPressure.include(sample.guestIOPressure)
	accumulator.lastUsage = sample.guestUsageMicroseconds
	accumulator.lastIOStat = sample.guestIOStat
}

func (pressure *Pressure) include(reading pressureReading) {
	pressure.SomeAvg10Max = max(pressure.SomeAvg10Max, reading.some)
	pressure.FullAvg10Max = max(pressure.FullAvg10Max, reading.full)
}

func (accumulator *hostAccumulator) finish() (HostSummary, error) {
	summary := accumulator.summary
	if summary.Samples < minimumSamples {
		return HostSummary{}, errors.New("window has fewer than two samples")
	}
	elapsed := summary.Last.Sub(summary.First)
	if elapsed <= 0 {
		return HostSummary{}, errors.New("window samples share one time")
	}
	usageSeconds := float64(accumulator.lastUsage-accumulator.firstUsage) / microsecondsPerSecond
	summary.GuestCPUCores = usageSeconds / elapsed.Seconds()
	readBytes := accumulator.lastIOStat.read - accumulator.firstIOStat.read
	writtenBytes := accumulator.lastIOStat.written - accumulator.firstIOStat.written
	summary.GuestReadMiB = float64(readBytes) / bytesPerMebibyte
	summary.GuestWriteMiB = float64(writtenBytes) / bytesPerMebibyte
	return summary, nil
}

// Elapsed returns the time between the first and last samples.
func (summary HostSummary) Elapsed() time.Duration {
	return summary.Last.Sub(summary.First)
}
