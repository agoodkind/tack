package resourcesummary

import (
	"context"
	"errors"
	"fmt"
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

type pressureReading struct {
	some float64
	full float64
}

type ioBytes struct {
	read    int64
	written int64
}

// parsePressure reads the avg10 value of the some and full lines in Linux pressure stall
// information format.
func parsePressure(ctx context.Context, lines []string) (pressureReading, error) {
	reading := pressureReading{some: 0, full: 0}
	if len(lines) == 0 {
		return reading, errors.New("pressure reading has no lines")
	}
	targets := map[string]*float64{
		pressureSomeKind: &reading.some,
		pressureFullKind: &reading.full,
	}
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < pressureFieldsMin {
			return reading, fmt.Errorf("pressure line %q has fewer than two fields", line)
		}
		text, found := strings.CutPrefix(fields[1], avg10Prefix)
		if !found {
			return reading, fmt.Errorf("pressure line %q has no avg10 field", line)
		}
		avg10, err := strconv.ParseFloat(text, floatBits)
		if err != nil {
			return reading, wrapError(ctx, "parse avg10 in "+line, err)
		}
		target, known := targets[fields[0]]
		if !known {
			return reading, fmt.Errorf("pressure line %q has an unknown kind", line)
		}
		*target = avg10
	}
	return reading, nil
}

// parseIOStat adds the rbytes and wbytes counters of every device line in a cgroup io.stat
// reading.
func parseIOStat(ctx context.Context, lines []string) (ioBytes, error) {
	total := ioBytes{read: 0, written: 0}
	if len(lines) == 0 {
		return total, errors.New("io.stat reading has no lines")
	}
	counters := map[string]*int64{
		ioReadKey:  &total.read,
		ioWriteKey: &total.written,
	}
	for _, line := range lines {
		for _, field := range strings.Fields(line) {
			key, text, found := strings.Cut(field, "=")
			counter, known := counters[key]
			if !found || !known {
				continue
			}
			value, err := strconv.ParseInt(text, decimalBase, int64Bits)
			if err != nil {
				return total, wrapError(ctx, "parse io.stat field "+field, err)
			}
			*counter += value
		}
	}
	return total, nil
}

// parseLoad reads the 1-minute load average from the first /proc/loadavg line.
func parseLoad(ctx context.Context, lines []string) (float64, error) {
	if len(lines) == 0 {
		return 0, errors.New("loadavg reading has no lines")
	}
	fields := strings.Fields(lines[0])
	if len(fields) == 0 {
		return 0, errors.New("loadavg line is empty")
	}
	load, err := strconv.ParseFloat(fields[0], floatBits)
	if err != nil {
		return 0, wrapError(ctx, "parse loadavg "+lines[0], err)
	}
	return load, nil
}
