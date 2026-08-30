// backup_restore_drill_fdb_counters.go reads how much work a FoundationDB
// restore has done out of `fdbrestore status`, and remembers the furthest it
// has ever got. That high-water mark is what the wait compares against, so
// only real forward motion counts as the restore still being alive.

package ops

import (
	"maps"
	"slices"
	"strconv"
	"strings"
)

// fdbRestoreProgressCounters are the fields of `fdbrestore status` that only
// ever count upwards while the restore does work, so a rise in one of them is
// evidence the restore moved. FoundationDB 7.4.6 prints one line per restore
// tag:
//
//	Tag: %s  UID: %s  State: %s  Blocks: %lld/%lld  BlocksInProgress: %lld  Files: %lld  BytesWritten: %lld  ApplyVersionLag: %lld  LastError: %s
//
// Blocks carries finished and total blocks, and the total is what moves while
// the restore is still enumerating the backup's files and no block has landed
// yet, so both halves count. BlocksInProgress falls as well as rises and
// ApplyVersionLag can fall, so neither is evidence of anything; Files is fixed
// once the restore is dispatched.
var fdbRestoreProgressCounters = map[string]bool{
	"blocks":       true,
	"blockstotal":  true,
	"byteswritten": true,
}

// parseFDBRestoreProgress reads the monotonic work counters out of one
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
// label that repeats keeps the larger value, so nothing here can make a
// counter appear to fall.
func recordFDBRestoreCounter(counters map[string]int64, name, value string) {
	done, total, paired := strings.Cut(value, "/")
	recordFDBCounterValue(counters, name, done)
	if paired {
		recordFDBCounterValue(counters, name+"total", total)
	}
}

func recordFDBCounterValue(counters map[string]int64, name, value string) {
	if !fdbRestoreProgressCounters[name] {
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

// fdbRestoreProgress remembers the highest value it has seen for every
// counter. High-water marks, rather than the last reading, are what stop a
// counter that rises and falls from looking like forward motion forever: it
// can pass its own peak only a bounded number of times, after which a wedged
// restore reads as stalled.
type fdbRestoreProgress struct {
	highWater map[string]int64
}

func newFDBRestoreProgress() *fdbRestoreProgress {
	return &fdbRestoreProgress{highWater: map[string]int64{}}
}

// observe records one status reading and reports whether it moved any counter
// past its high-water mark. The first reading that carries counters at all
// counts as movement, because the restore registering is itself progress.
func (p *fdbRestoreProgress) observe(status string) bool {
	advanced := false
	for name, value := range parseFDBRestoreProgress(status) {
		if previous, seen := p.highWater[name]; !seen || value > previous {
			p.highWater[name] = value
			advanced = true
		}
	}
	return advanced
}

// summary renders the counters in a stable order for a log line or a stall
// report. It says so when it never read one, because a status the drill could
// not read at all is a different failure from a restore that moved and then
// stopped.
func (p *fdbRestoreProgress) summary() string {
	if len(p.highWater) == 0 {
		return "no progress counters were ever readable from fdbrestore status"
	}
	parts := make([]string, 0, len(p.highWater))
	for _, name := range slices.Sorted(maps.Keys(p.highWater)) {
		parts = append(parts, name+"="+strconv.FormatInt(p.highWater[name], 10))
	}
	return strings.Join(parts, " ")
}
