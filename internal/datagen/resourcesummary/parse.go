package resourcesummary

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	pressureSomeKind  = "some"
	pressureFullKind  = "full"
	avg10Prefix       = "avg10="
	ioReadKey         = "rbytes"
	ioWriteKey        = "wbytes"
	pressureFieldsMin = 2
	floatBits         = 64
	decimalBase       = 10
	int64Bits         = 64
)

var (
	errPressureLineMissing = errors.New("pressure line is missing")
	errMeasurementRange    = errors.New("value must be finite and not negative")
	errIOCounterMissing    = errors.New("io.stat counter is missing")
)

type pressureReading struct {
	some float64
	full float64
}

type ioBytes struct {
	read    int64
	written int64
}

// parsePressure reads the avg10 value of the some and full lines in Linux pressure stall
// information format. It returns an error unless lines contains both kinds.
func parsePressure(lines []string) (pressureReading, error) {
	reading := pressureReading{some: 0, full: 0}
	if len(lines) == 0 {
		return reading, errors.New("pressure reading has no lines")
	}
	targets := map[string]*float64{
		pressureSomeKind: &reading.some,
		pressureFullKind: &reading.full,
	}
	seenKinds := map[string]bool{}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < pressureFieldsMin {
			return reading, fmt.Errorf("pressure line %q has fewer than two fields", line)
		}
		text, found := strings.CutPrefix(fields[1], avg10Prefix)
		if !found {
			return reading, fmt.Errorf("pressure line %q has no avg10 field", line)
		}
		avg10, err := parseMeasurement("parse avg10 in "+strconv.Quote(line), text)
		if err != nil {
			return reading, err
		}
		target, known := targets[fields[0]]
		if !known {
			return reading, fmt.Errorf("pressure line %q has an unknown kind", line)
		}
		*target = avg10
		seenKinds[fields[0]] = true
	}
	for kind := range targets {
		if !seenKinds[kind] {
			step := fmt.Sprintf("find %s line in %q", kind, lines)
			return reading, wrapError(step, errPressureLineMissing)
		}
	}
	return reading, nil
}

// parseMeasurement parses a pressure or load value and rejects a value that is not finite or
// is negative. The returned error starts with step.
func parseMeasurement(step string, text string) (float64, error) {
	value, err := strconv.ParseFloat(text, floatBits)
	if err != nil {
		return 0, wrapError(step, err)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, wrapError(step, errMeasurementRange)
	}
	return value, nil
}

// parseIOStat adds the rbytes and wbytes counters of every device line in a cgroup io.stat
// reading. It returns an error unless every device line contains both counters.
func parseIOStat(lines []string) (ioBytes, error) {
	total := ioBytes{read: 0, written: 0}
	if len(lines) == 0 {
		return total, errors.New("io.stat reading has no lines")
	}
	for _, line := range lines {
		device, err := parseIOStatLine(line)
		if err != nil {
			return total, err
		}
		total.read += device.read
		total.written += device.written
	}
	return total, nil
}

func parseIOStatLine(line string) (ioBytes, error) {
	device := ioBytes{read: 0, written: 0}
	counters := map[string]*int64{
		ioReadKey:  &device.read,
		ioWriteKey: &device.written,
	}
	seenKeys := map[string]bool{}
	for _, field := range strings.Fields(line) {
		key, text, found := strings.Cut(field, "=")
		counter, known := counters[key]
		if !found || !known {
			continue
		}
		value, err := strconv.ParseInt(text, decimalBase, int64Bits)
		if err != nil {
			return device, wrapError("parse io.stat field "+field, err)
		}
		*counter += value
		seenKeys[key] = true
	}
	for _, key := range []string{ioReadKey, ioWriteKey} {
		if !seenKeys[key] {
			step := fmt.Sprintf("find %s in io.stat line %q", key, line)
			return device, wrapError(step, errIOCounterMissing)
		}
	}
	return device, nil
}

// parseLoad reads the 1-minute load average from the first /proc/loadavg line.
func parseLoad(lines []string) (float64, error) {
	if len(lines) == 0 {
		return 0, errors.New("loadavg reading has no lines")
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 {
		return 0, errors.New("loadavg line is empty")
	}
	return parseMeasurement("parse loadavg "+strconv.Quote(lines[0]), fields[0])
}
