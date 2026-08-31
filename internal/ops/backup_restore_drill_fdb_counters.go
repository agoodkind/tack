// backup_restore_drill_fdb_counters.go reads how much work a FoundationDB
// restore has done out of `fdbrestore status`, and remembers the furthest each
// counter has ever got in the one direction that counter moves while work is
// happening. Those marks are what the wait compares against, so only real
// forward motion counts as the restore still being alive.

package ops

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// fdbRestoreRisingCounters are the fields of `fdbrestore status` that only ever
// count upwards while the restore does work, so a rise in one of them is
// evidence the restore moved. FoundationDB 7.4.6 prints one line per restore
// tag:
//
//	Tag: %s  UID: %s  State: %s  Blocks: %lld/%lld  BlocksInProgress: %lld  Files: %lld  BytesWritten: %lld  ApplyVersionLag: %lld  LastError: %s
//
// Blocks carries finished and total blocks, and the total is what moves while
// the restore is still enumerating the backup's files and no block has landed
// yet, so both halves count. BlocksInProgress is excluded because it rises and
// falls as blocks are dispatched and retired, so it moves in no fixed
// direction and no reading of it is evidence of anything; Files is fixed once
// the restore is dispatched.
var fdbRestoreRisingCounters = map[string]bool{
	"blocks":       true,
	"blockstotal":  true,
	"byteswritten": true,
}

// fdbRestoreFallingCounters are the fields that only ever count downwards while
// the restore does work, so a fall in one of them is the evidence a rise is for
// the counters above. ApplyVersionLag is how far the restore's applied version
// still trails its target. Applying the backup's mutation log is exactly the
// phase where no new block lands and no new byte is written, so a restore in it
// shows every rising counter standing still while the lag falls: reading only
// rises there is what made a healthy large restore look wedged and get killed
// once its quiet last phase outran the inactivity window.
var fdbRestoreFallingCounters = map[string]bool{
	"applyversionlag": true,
}

// parseFDBRestoreProgress reads the directional work counters out of one
// `fdbrestore status` output. A status holding none of them yields no
// counters, which the wait reads as no progress rather than as an error: an
// unreadable status then ends as a named stall carrying the raw reading,
// instead of as an unbounded wait.
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

// recordFDBRestoreCounter stores a counter's value, and stores the second half
// under a "total" name when the value is the done/total pair Blocks prints. A
// label that repeats across the status's per-tag lines keeps the larger value,
// which is the conservative reading in both directions: a rising counter cannot
// be made to look like it fell, and a falling one only signals once the worst
// tag's value has come down.
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

// normalizeFDBCounterName lowercases a status label and drops everything that
// is not a letter or a digit, so cosmetic punctuation around a label does not
// quietly drop the counter it names.
func normalizeFDBCounterName(label string) string {
	var name strings.Builder
	for _, symbol := range strings.ToLower(label) {
		if (symbol >= 'a' && symbol <= 'z') || (symbol >= '0' && symbol <= '9') {
			name.WriteRune(symbol)
		}
	}
	return name.String()
}

// fdbRestoreProgress remembers the furthest every counter has moved in its own
// direction: the highest value seen for a rising counter, the lowest for a
// falling one. Marks, rather than the last reading, are what stop a value that
// moves both ways from looking like forward motion forever. Every mark is
// one-directional, so a counter signals only while it is setting a new extreme
// and a value that oscillates signals nothing after its first excursion.
type fdbRestoreProgress struct {
	highWater map[string]int64
	lowWater  map[string]int64
}

func newFDBRestoreProgress() *fdbRestoreProgress {
	return &fdbRestoreProgress{highWater: map[string]int64{}, lowWater: map[string]int64{}}
}

// observe records one status reading and reports whether it moved any counter
// past its mark. The first reading that carries counters at all counts as
// movement, because the restore registering is itself progress.
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

// observeRise moves a rising counter's high-water mark, which never falls. The
// engine only ever increments these, so the mark can be passed once per
// increment and a restore doing no work passes it never.
func (p *fdbRestoreProgress) observeRise(name string, value int64) bool {
	previous, seen := p.highWater[name]
	if seen && value <= previous {
		return false
	}
	p.highWater[name] = value
	return true
}

// observeFall moves a falling counter's low-water mark, which never rises. A
// negative reading is ignored, so the mark is a whole number that strictly
// decreases on every signal and is floored at zero: one restore can therefore
// produce at most as many signals as its first reading's value, which bounds
// them however long it runs. A lag that climbs back up, or oscillates, moves
// nothing.
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

// summary renders every mark in a stable order for a log line or a stall
// report. It says so when it never read one, because a status the drill could
// not read at all is a different failure from a restore that moved and then
// stopped. The two mark sets name disjoint counters, so one merged reading is
// unambiguous.
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
