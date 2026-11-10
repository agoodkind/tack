//go:build runtimeaudit

package integration_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/apple/foundationdb/bindings/go/src/fdb"

	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/test/integration"
)

const (
	retireValueLimit     = 100_000
	nearLimitRecordBytes = retireValueLimit - 20
	nearLimitRecordCount = 2
	escapedRecordFill    = "<"
	plainRecordFill      = "x"
)

// TestSearchSessionAuditRetireObservationAboveValueLimit stores two valid
// records with future deadlines, each 20 bytes below the FoundationDB
// value limit. One event contains many < characters, which JSON encoding
// escapes. The other event contains plain characters, and its record
// gains an observation field. The test checks that each encoded value
// with an observation exceeds the limit. One RetireIndexSessions call
// must finish retirement without an error and clear both records.
// The production relay and audit consumer must retain every stored
// value in audit.events_dlq.
func TestSearchSessionAuditRetireObservationAboveValueLimit(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-observation-limit"
	beginRetiringRebuild(t, stores, index)
	mark := searchOutboxTail(t, stores)
	deadline := clock.Now().Add(retireWriterOffset).UTC()
	expected := map[string][]byte{}
	var firstKey fdb.Key
	for _, fill := range []string{escapedRecordFill, plainRecordFill} {
		value := nearLimitAttemptValue(t, deadline, fill)
		requireObservationAboveLimit(t, value)
		key := retireAttemptKey(t, index)
		if firstKey == nil {
			firstKey = key
		}
		integration.ReplaceRawValue(t, key, value)
		expected[digestOf(value)] = value
	}

	remaining, err := rebuilds.RetireIndexSessions(
		integration.WithTestOperation(t, t.Context()),
		index,
		retireSliceLimit,
	)
	if err != nil || remaining {
		t.Fatalf(
			"RetireIndexSessions of %s with oversized observations = %t, %v, want a finished retirement",
			index,
			remaining,
			err,
		)
	}
	attemptRecordValues(t, 0)
	if events := invalidAttemptEvents(t, stores, mark); len(events) != nearLimitRecordCount {
		t.Fatalf(
			"index %s has %d invalid attempt events, want %d",
			index,
			len(events),
			nearLimitRecordCount,
		)
	}
	retainedIDs := retainedAttemptEvents(t, stores, mark)
	stack := startRuntimeAuditStack(t)
	startRetireRelay(t, stack)
	requireRetainedRecords(t, stack.waitForEventDeadLetters(t, retainedIDs), expected, firstKey)
}

// nearLimitAttemptValue returns a valid attempt record with
// nearLimitRecordBytes bytes. It joins the JSON text directly and leaves
// fill unescaped.
func nearLimitAttemptValue(t *testing.T, deadline time.Time, fill string) []byte {
	t.Helper()
	encodedDeadline, err := json.Marshal(deadline)
	if err != nil {
		t.Fatalf("encode the deadline: %v", err)
	}
	prefix := `{"deadline":` + string(encodedDeadline) + `,"event":{"filler":"`
	suffix := `"}}`
	return []byte(
		prefix + strings.Repeat(fill, nearLimitRecordBytes-len(prefix)-len(suffix)) + suffix,
	)
}

// requireObservationAboveLimit requires value to fit within the limit.
// It requires the encoding of value with an observation time to exceed
// the limit.
func requireObservationAboveLimit(t *testing.T, value []byte) {
	t.Helper()
	var fields storedAttemptFields
	if err := json.Unmarshal(value, &fields); err != nil {
		t.Fatalf("decode the near-limit record: %v", err)
	}
	fields.ObservedAt = clock.Now().UTC()
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode the near-limit record: %v", err)
	}
	if len(value) > retireValueLimit || len(encoded) <= retireValueLimit {
		t.Fatalf(
			"the stored record has %d bytes and its observed encoding has %d bytes, want at most %d and more than %d",
			len(value),
			len(encoded),
			retireValueLimit,
			retireValueLimit,
		)
	}
}
