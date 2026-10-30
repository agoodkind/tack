package resourcesummary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	hostCPUPressurePath = "/proc/pressure/cpu"
	hostIOPressurePath  = "/proc/pressure/io"
	hostLoadPath        = "/proc/loadavg"
	guestCPUPressureKey = "cpu.pressure"
	guestIOPressureKey  = "io.pressure"
	minimumSamples      = 2
)

type hostAccumulator struct {
	guestID     string
	summary     HostSummary
	firstUsage  int64
	lastUsage   int64
	firstIOStat ioBytes
	lastIOStat  ioBytes
}

func newHostAccumulator(guestID string) *hostAccumulator {
	return &hostAccumulator{
		guestID:     guestID,
		summary:     HostSummary{},
		firstUsage:  0,
		lastUsage:   0,
		firstIOStat: ioBytes{read: 0, written: 0},
		lastIOStat:  ioBytes{read: 0, written: 0},
	}
}

func (accumulator *hostAccumulator) add(ctx context.Context, line samplerLine) error {
	guest, found := line.CPUDisk.Guests[accumulator.guestID]
	if !found {
		return fmt.Errorf("sample %s has no guest %s", line.Time, accumulator.guestID)
	}
	usage, err := guest.CPU.UsageUsec.Int64()
	if err != nil {
		return wrapError(ctx, "parse usage_usec at "+line.Time.String(), err)
	}
	ioStat, err := parseIOStat(ctx, guest.IO)
	if err != nil {
		return err
	}
	err = accumulator.addPressure(ctx, line)
	if err != nil {
		return err
	}
	load, err := parseLoad(ctx, line.CPUDisk.Host[hostLoadPath])
	if err != nil {
		return err
	}
	memAvailableGiB := float64(line.Meminfo.MemAvailable) / kibibytesPerGibibyte
	summary := &accumulator.summary
	if summary.Samples == 0 {
		summary.First = line.Time
		summary.MemAvailableMinGiB = memAvailableGiB
		accumulator.firstUsage = usage
		accumulator.firstIOStat = ioStat
	}
	summary.Samples++
	summary.Last = line.Time
	summary.MemAvailableMinGiB = min(summary.MemAvailableMinGiB, memAvailableGiB)
	summary.LoadOneMinuteMax = max(summary.LoadOneMinuteMax, load)
	accumulator.lastUsage = usage
	accumulator.lastIOStat = ioStat
	return nil
}

func (accumulator *hostAccumulator) addPressure(ctx context.Context, line samplerLine) error {
	guestPressure := line.CPUDisk.Pressure[accumulator.guestID]
	sources := []struct {
		lines  []string
		target *Pressure
	}{
		{lines: strings.Split(line.Pressure, "\n"), target: &accumulator.summary.MemoryPressure},
		{lines: line.CPUDisk.Host[hostCPUPressurePath], target: &accumulator.summary.CPUPressure},
		{lines: line.CPUDisk.Host[hostIOPressurePath], target: &accumulator.summary.IOPressure},
		{lines: guestPressure[guestCPUPressureKey], target: &accumulator.summary.GuestCPUPressure},
		{lines: guestPressure[guestIOPressureKey], target: &accumulator.summary.GuestIOPressure},
	}
	for _, source := range sources {
		reading, err := parsePressure(ctx, source.lines)
		if err != nil {
			return err
		}
		source.target.SomeAvg10Max = max(source.target.SomeAvg10Max, reading.some)
		source.target.FullAvg10Max = max(source.target.FullAvg10Max, reading.full)
	}
	return nil
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
