// The drill reads FoundationDB restore work counters from fdbrestore status.
// It compares each reading with the highest rising value or lowest falling
// value previously observed. Only a new extreme counts as further progress.

package ops

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// fdbRestoreRisingCounters selects status fields that increase during restore
// work. The original FoundationDB 7.4.6 observation recorded one line per tag:
//
//	Tag: %s  UID: %s  State: %s  Blocks: %lld/%lld  BlocksInProgress: %lld  Files: %lld  BytesWritten: %lld  ApplyVersionLag: %lld  LastError: %s
//
// Blocks reports completed and total blocks. The total can increase while the
// restore enumerates backup files before completing a block; both values
// count as progress. BlocksInProgress increases and decreases as the engine
// dispatches and finishes blocks, and cannot establish directional progress.
// Files stays fixed after dispatch.
var fdbRestoreRisingCounters = map[string]bool{
	"blocks":       true,
	"blockstotal":  true,
	"byteswritten": true,
}

// fdbRestoreFallingCounters selects fields that decrease during restore work.
// ApplyVersionLag measures the difference between the target version and the
// applied version. The mutation-log phase can reduce that difference without
// completing another block or writing another byte.
//
// The original defect occurred when a healthy large restore spent longer than
// the inactivity window in that final phase. A wait that checked only rising
// counters treated the restore as stalled and stopped it.
var fdbRestoreFallingCounters = map[string]bool{
	"applyversionlag": true,
}

// parseFDBRestoreProgress reads directional counters from one fdbrestore
// status response. An unparseable response returns an empty map. The wait
// treats it as a poll without progress and eventually reports a stall if
// neither completion nor progress occurs.
//
// If every response lacks readable counters, the stall report states that
// condition. Errors and logs use the parsed counters rather than raw status.
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

// recordFDBRestoreCounter records a value and, for the Blocks done/total pair,
// records the total under the counter name plus "total". Repeated labels on
// per-tag status lines retain the largest value.
//
// This aggregation cannot make a rising counter appear to decrease. A falling
// counter signals progress only after the largest tag value decreases.
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

// normalizeFDBCounterName lowercases a status label and retains only ASCII
// letters and digits. Punctuation around a recognized label does not prevent
// the parser from recognizing that counter.
func normalizeFDBCounterName(label string) string {
	var name strings.Builder
	for _, symbol := range strings.ToLower(label) {
		if (symbol >= 'a' && symbol <= 'z') || (symbol >= '0' && symbol <= '9') {
			name.WriteRune(symbol)
		}
	}
	return name.String()
}

// fdbRestoreProgress tracks the highest rising counter values and the lowest
// falling counter values. Each counter signals progress only when it sets a
// new extreme. Repeated oscillation within previously observed values does
// not keep resetting the inactivity timer.
type fdbRestoreProgress struct {
	highWater map[string]int64
	lowWater  map[string]int64
}

func newFDBRestoreProgress() *fdbRestoreProgress {
	return &fdbRestoreProgress{highWater: map[string]int64{}, lowWater: map[string]int64{}}
}

// observe records one status response and reports whether a counter sets a
// new extreme. The first valid value for a counter counts as progress because
// the restore has begun reporting that counter.
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

// observeRise updates a counter's highest value only when a reading exceeds
// the previous maximum. The engine's rising work counters signal each new
// increment once; an unchanged reading does not signal progress.
func (p *fdbRestoreProgress) observeRise(name string, value int64) bool {
	previous, seen := p.highWater[name]
	if seen && value <= previous {
		return false
	}
	p.highWater[name] = value
	return true
}

// observeFall ignores negative readings and records each new minimum. The
// first nonnegative value establishes the mark; every later progress signal
// requires a smaller integer, with zero as the lower bound.
//
// After that initial reading, a counter can signal at most as many times as
// its initial value. A later increase or repeated oscillation above the
// minimum does not signal progress.
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

// summary renders all marks in sorted order for logs and stall reports. It
// reports when the drill has never read a counter, distinguishing that case
// from a restore that progressed and then stopped. Rising and falling maps
// use different counter names, and the merged summary preserves both sets.
func (p *fdbRestoreProgress) summary() string {
	marks := make(map[string]int64, len(p.highWater)+len(p.lowWater))
	maps.Copy(marks, p.highWater)
	maps.Copy(marks, p.lowWater)
	if len(marks) == 0 {
		return "no progress counters were ever readable from fdbrestore status"
	}
	parts := make([]string, 0, len(marks))
	for _, name := range slices.Sorted(maps.Keys(marks)) {
		parts = append(parts, name+"="+strconv.FormatInt(marks[name], 10))
	}
	return strings.Join(parts, " ")
}
