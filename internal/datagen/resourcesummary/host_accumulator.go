package resourcesummary

import (
	"errors"
	"fmt"
	"time"
)

const (
	minimumSamples = 2
	guestUsageKey  = "usage_usec"
)

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

func (accumulator *hostAccumulator) add(sample hostSample) error {
	summary := &accumulator.summary
	if summary.Samples == 0 {
		summary.First = sample.time
		summary.MemAvailableMinGiB = sample.memAvailableGiB
		accumulator.firstUsage = sample.guestUsageMicroseconds
		accumulator.firstIOStat = sample.guestIOStat
	} else {
		err := accumulator.checkCounters(sample)
		if err != nil {
			return err
		}
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
	return nil
}

// checkCounters compares sample with the previous sample. It rejects a
// decrease in a cumulative counter as a reset. The summary does not estimate
// activity across a reset.
func (accumulator *hostAccumulator) checkCounters(sample hostSample) error {
	counters := []struct {
		name     string
		previous int64
		current  int64
	}{
		{guestUsageKey, accumulator.lastUsage, sample.guestUsageMicroseconds},
		{ioReadKey, accumulator.lastIOStat.read, sample.guestIOStat.read},
		{ioWriteKey, accumulator.lastIOStat.written, sample.guestIOStat.written},
	}
	for _, counter := range counters {
		if counter.current < counter.previous {
			return fmt.Errorf("guest %s decreased from %d to %d",
				counter.name, counter.previous, counter.current)
		}
	}
	return nil
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
//
// It subtracts First from Last without checking the sample count.
//
// For example, a zero-value summary returns a zero duration:
//
//	elapsed := (HostSummary{}).Elapsed()
func (summary HostSummary) Elapsed() time.Duration {
	return summary.Last.Sub(summary.First)
}
