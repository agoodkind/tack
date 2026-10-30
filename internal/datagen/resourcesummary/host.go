// Package resourcesummary summarizes the CPU, memory, and disk readings that the hypervisor
// sampler and the search-mixed workload saved during a QA workload window.
package resourcesummary

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"
)

const (
	kibibytesPerGibibyte  = 1024 * 1024
	bytesPerMebibyte      = 1024 * 1024
	microsecondsPerSecond = 1_000_000
	initialLineBytes      = 1 << 20
	maxLineBytes          = 64 << 20
)

// Window is a closed time range of hypervisor samples.
type Window struct {
	Start time.Time
	End   time.Time
}

// Pressure is the largest avg10 value of the some and full pressure stall lines.
type Pressure struct {
	SomeAvg10Max float64
	FullAvg10Max float64
}

// HostSummary reports the hypervisor and search guest readings inside one window.
type HostSummary struct {
	Samples int
	// FailedSamples counts samples inside the window that carry an error.
	FailedSamples      int
	First              time.Time
	Last               time.Time
	MemAvailableMinGiB float64
	MemoryPressure     Pressure
	CPUPressure        Pressure
	IOPressure         Pressure
	LoadOneMinuteMax   float64
	// GuestCPUCores is the guest cgroup CPU time between the first and last samples divided
	// by the wall time between them.
	GuestCPUCores    float64
	GuestCPUPressure Pressure
	GuestIOPressure  Pressure
	GuestReadMiB     float64
	GuestWriteMiB    float64
}

type samplerLine struct {
	Time     time.Time      `json:"time"`
	Error    string         `json:"error"`
	Meminfo  samplerMeminfo `json:"meminfo_kib"`
	Pressure string         `json:"pressure"`
	CPUDisk  samplerCPUDisk `json:"cpu_disk"`
}

type samplerMeminfo struct {
	MemAvailable int64 `json:"MemAvailable"`
}

type samplerCPUDisk struct {
	Host     map[string][]string            `json:"host"`
	Guests   map[string]samplerGuest        `json:"guests"`
	Pressure map[string]map[string][]string `json:"pressure"`
}

type samplerGuest struct {
	CPU samplerGuestCPU `json:"cpu"`
	IO  []string        `json:"io"`
}

type samplerGuestCPU struct {
	// UsageUsec accepts a JSON number or a JSON string that contains a number.
	UsageUsec json.Number `json:"usage_usec"`
}

// SummarizeHost reads a sampler JSON Lines file and summarizes the samples inside window
// for the host and for the guest with guestID. It counts and skips samples that carry an
// error.
func SummarizeHost(
	ctx context.Context,
	path string,
	window Window,
	guestID string,
) (HostSummary, error) {
	file, err := os.Open(path)
	if err != nil {
		return HostSummary{}, wrapError(ctx, "open sampler file "+path, err)
	}
	defer closeFile(ctx, file)
	accumulator := newHostAccumulator(guestID)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, initialLineBytes), maxLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		var line samplerLine
		decodeErr := json.Unmarshal(scanner.Bytes(), &line)
		if decodeErr != nil {
			message := fmt.Sprintf("decode %s line %d", path, lineNumber)
			return HostSummary{}, wrapError(ctx, message, decodeErr)
		}
		if line.Time.Before(window.Start) || line.Time.After(window.End) {
			continue
		}
		if line.Error != "" {
			accumulator.summary.FailedSamples++
			continue
		}
		addErr := accumulator.add(ctx, line)
		if addErr != nil {
			return HostSummary{}, addErr
		}
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		return HostSummary{}, wrapError(ctx, "scan sampler file "+path, scanErr)
	}
	slog.InfoContext(ctx, "resource_summary.sampler_read",
		slog.String("path", path), slog.Int("samples", accumulator.summary.Samples),
		slog.Int("failed_samples", accumulator.summary.FailedSamples))
	return accumulator.finish()
}

func closeFile(ctx context.Context, file *os.File) {
	closeErr := file.Close()
	if closeErr != nil {
		slog.ErrorContext(ctx, "resource_summary.close_failed",
			slog.String("path", file.Name()), slog.String("err", closeErr.Error()))
	}
}

func wrapError(ctx context.Context, step string, err error) error {
	slog.ErrorContext(ctx, "resource_summary.read_failed",
		slog.String("step", step), slog.String("err", err.Error()))
	return fmt.Errorf("%s: %w", step, err)
}
