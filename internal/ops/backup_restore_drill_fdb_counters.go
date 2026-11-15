// FoundationDB restore monitoring reads work counters from `fdbrestore status`
// and records each counter's highest or lowest value to detect progress.

package ops

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// fdbRestoreRisingCounters selects counters that increase during restore work.
// FoundationDB 7.4.6 prints one line per restore tag:
//
//	Tag: %s  UID: %s  State: %s  Blocks: %lld/%lld  BlocksInProgress: %lld  Files: %lld  BytesWritten: %lld  ApplyVersionLag: %lld  LastError: %s
//
// Blocks reports finished and total blocks. The total increases during file
// enumeration before any block finishes. Both values indicate progress.
// BlocksInProgress can increase and decrease during dispatch and completion;
// it cannot establish progress. Files remains fixed after dispatch.
var fdbRestoreRisingCounters = map[string]bool{
	"blocks":       true,
	"blockstotal":  true,
	"byteswritten": true,
}

// fdbRestoreFallingCounters selects counters that decrease during restore work.
// ApplyVersionLag measures the difference between the applied and target
// versions. It decreases during mutation log application even when block and
// byte counts remain constant. Rising counters alone would incorrectly
// indicate a stall during this phase.
var fdbRestoreFallingCounters = map[string]bool{
	"applyversionlag": true,
}

// parseFDBRestoreProgress reads work counters from `fdbrestore status`.
// An output without readable counters does not indicate progress. The wait
// eventually reports a stall rather than waiting indefinitely. Errors and logs
// include only counters, never raw status output.
func parseFDBRestoreProgress(status string) map[string]int64 {
	counters := map[string]int64{}

	fields := strings.Fields(status)
	for i, field := range fields {
		label, isLabel := strings.CutSuffix(field, ":")
		if !isLabel || i+1 == len(fields) {
			continue
		}

		recordFDBRestoreCounter(counters, normalizeFDBCounterName(label), fields[i+1])
	}

	return counters
}

// recordFDBRestoreCounter stores the finished value and, for a Blocks pair,
// the total under a separate label. Repeated per-tag labels select the maximum
// value. Rising counters cannot appear to decrease; falling counters indicate
// progress only after the highest per-tag value decreases.
func recordFDBRestoreCounter(counters map[string]int64, name, value string) {
	done, total, paired := strings.Cut(value, "/")
	recordFDBCounterValue(counters, name, done)
	if paired {
		recordFDBCounterValue(counters, name+"total", total)
	}
}

func recordFDBCounterValue(counters map[string]int64, name, value string) {
	if !fdbRestoreRisingCounters[name] && !fdbRestoreFallingCounters[name] {
		return
	}

	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return
	}

	if existing, seen := counters[name]; !seen || parsed > existing {
		counters[name] = parsed
	}
}

// normalizeFDBCounterName lowercases labels and removes nonalphanumeric
// characters to recognize counters despite surrounding punctuation.
func normalizeFDBCounterName(label string) string {
	var name strings.Builder

	for _, symbol := range strings.ToLower(label) {
		if (symbol >= 'a' && symbol <= 'z') || (symbol >= '0' && symbol <= '9') {
			name.WriteRune(symbol)
		}
	}

	return name.String()
}

// fdbRestoreProgress records each rising counter's maximum and each falling
// counter's minimum. Oscillating values indicate progress only when they
// establish a new maximum or minimum.
type fdbRestoreProgress struct {
	highWater map[string]int64
	lowWater  map[string]int64
}

func newFDBRestoreProgress() *fdbRestoreProgress {
	return &fdbRestoreProgress{highWater: map[string]int64{}, lowWater: map[string]int64{}}
}

// observe records counters and reports progress when any value establishes a
// new maximum or minimum. The first readable counters establish initial
// progress.
func (p *fdbRestoreProgress) observe(status string) bool {
	advanced := false

	for name, value := range parseFDBRestoreProgress(status) {
		var moved bool
		if fdbRestoreFallingCounters[name] {
			moved = p.observeFall(name, value)
		} else {
			moved = p.observeRise(name, value)
		}

		if moved {
			advanced = true
		}
	}

	return advanced
}

// observeRise updates a counter's maximum only when the value increases.
func (p *fdbRestoreProgress) observeRise(name string, value int64) bool {
	previous, seen := p.highWater[name]
	if seen && value <= previous {
		return false
	}

	p.highWater[name] = value

	return true
}

// observeFall ignores negative values and updates a counter's minimum only
// when the value decreases. Nonnegative integer values bound the number of
// progress signals after the initial reading. Increases and oscillations
// above the minimum do not indicate progress.
func (p *fdbRestoreProgress) observeFall(name string, value int64) bool {
	if value < 0 {
		return false
	}

	previous, seen := p.lowWater[name]
	if seen && value >= previous {
		return false
	}

	p.lowWater[name] = value

	return true
}

// summary prints recorded extremes in a stable order. It distinguishes
// unreadable counters from counters that indicated progress before a stall.
// The rising and falling maps track different counters.
func (p *fdbRestoreProgress) summary() string {
	marks := make(map[string]int64, len(p.highWater)+len(p.lowWater))
	maps.Copy(marks, p.highWater)
	maps.Copy(marks, p.lowWater)
	if len(marks) == 0 {
		return "fdbrestore status never reported readable progress counters"
	}

	parts := make([]string, 0, len(marks))
	for _, name := range slices.Sorted(maps.Keys(marks)) {
		parts = append(parts, name+"="+strconv.FormatInt(marks[name], 10))
	}

	return strings.Join(parts, " ")
}
