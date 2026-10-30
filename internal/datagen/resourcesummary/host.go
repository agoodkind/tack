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
		return HostSummary{}, wrapError("open sampler file "+path, err)
	}
	defer closeFile(ctx, file)
	accumulator := newHostAccumulator()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, initialLineBytes), maxLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		var line samplerLine
		decodeErr := json.Unmarshal(scanner.Bytes(), &line)
		if decodeErr != nil {
			message := fmt.Sprintf("decode %s line %d", path, lineNumber)
			return HostSummary{}, wrapError(message, decodeErr)
		}
		if line.Time.Before(window.Start) || line.Time.After(window.End) {
			continue
		}
		if line.Error != "" {
			accumulator.summary.FailedSamples++
			continue
		}
		sample, parseErr := parseSample(line, guestID)
		if parseErr != nil {
			return HostSummary{}, parseErr
		}
		accumulator.add(sample)
	}
	scanErr := scanner.Err()
	if scanErr != nil {
		return HostSummary{}, wrapError("scan sampler file "+path, scanErr)
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

// readError reports the input step that failed and its cause. The caller logs it.
type readError struct {
	step  string
	cause error
}

func (failure *readError) Error() string {
	return failure.step + ": " + failure.cause.Error()
}

func (failure *readError) Unwrap() error {
	return failure.cause
}

func wrapError(step string, err error) error {
	return &readError{step: step, cause: err}
}
