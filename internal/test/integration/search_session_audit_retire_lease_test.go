//go:build runtimeaudit

package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireWriterOffset    = time.Hour
	retireFarOffset       = 10 * 365 * 24 * time.Hour
	retireLeaseRecordSize = 3
)

// TestSearchSessionAuditRetireLeaseRunsFromFirstObservation stores three
// attempt records. One has a deadline and event times one hour ahead.
// Another has them ten years ahead. The third has them one hour ahead and
// has a future ObservedAt. The first RetireIndexSessions call must store
// ObservedAt once at or before the current time in each record and keep all
// three active. A second call during the lease must not change the records.
// After the lease, a third call must save three incomplete events, save
// zero invalid events, and finish retirement. The test writes the times
// directly. A shifted store clock would also change transaction deadlines.
func TestSearchSessionAuditRetireLeaseRunsFromFirstObservation(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-lease-observation"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	hooks := installRetireLogHooks(
		t,
		session.ID.String(),
		map[string]func(){
			retireCleanupFailedMessage: func() { setContainerPaused(t, container, true) },
			retireCodesFailedMessage:   func() { setContainerPaused(t, container, false) },
		},
	)
	if _, err := rebuilds.RetireIndexSessions(
		withFutureOperation(t, t.Context()),
		index,
		retireSliceLimit,
	); err == nil {
		t.Fatalf("RetireIndexSessions of %s under the failing operation returned no error", index)
	}
	hooks.requireFinished(t)
	stored := retireFamilyValue(t, retireAttemptFamily)
	future := clock.Now().Add(retireWriterOffset).UTC()
	integration.ReplaceRawValue(
		t,
		retireFamilyKey(t, retireAttemptFamily),
		shiftedAttemptRecord(t, stored, retireWriterOffset, time.Time{}),
	)
	integration.ReplaceRawValue(
		t,
		retireAttemptKey(t, index),
		shiftedAttemptRecord(t, stored, retireFarOffset, time.Time{}),
	)
	integration.ReplaceRawValue(
		t,
		retireAttemptKey(t, index),
		shiftedAttemptRecord(t, stored, retireWriterOffset, future),
	)

	observedBefore := clock.Now()
	remaining, err := rebuilds.RetireIndexSessions(
		integration.WithTestOperation(t, t.Context()),
		index,
		retireSliceLimit,
	)
	if err != nil || !remaining {
		t.Fatalf(
			"RetireIndexSessions of %s at first observation = %t, %v, want remaining work",
			index,
			remaining,
			err,
		)
	}
	observed := attemptRecordValues(t, retireLeaseRecordSize)
	for _, value := range observed {
		var record struct {
			ObservedAt time.Time `json:"observed_at"`
		}
		if err := json.Unmarshal(
			value,
			&record,
		); err != nil || record.ObservedAt.Before(observedBefore.Add(-time.Second)) ||
			record.ObservedAt.After(clock.Now()) {
			t.Fatalf(
				"attempt record %s, %v, want an observation time between %s and the current time",
				value,
				err,
				observedBefore,
			)
		}
	}
	remaining, err = rebuilds.RetireIndexSessions(
		integration.WithTestOperation(t, t.Context()),
		index,
		retireSliceLimit,
	)
	if err != nil || !remaining {
		t.Fatalf(
			"RetireIndexSessions of %s during the lease = %t, %v, want remaining work",
			index,
			remaining,
			err,
		)
	}
	for position, value := range attemptRecordValues(t, retireLeaseRecordSize) {
		if !bytes.Equal(value, observed[position]) {
			t.Fatalf(
				"attempt record %d changed during the lease from %s to %s",
				position,
				observed[position],
				value,
			)
		}
	}
	requireLeaseEvents(t, stores, mark, session.ID.String(), 0)

	waitUntilTime(t, clock.Now().Add(retireAttemptWait))
	remaining, err = rebuilds.RetireIndexSessions(
		integration.WithTestOperation(t, t.Context()),
		index,
		retireSliceLimit,
	)
	if err != nil || remaining {
		t.Fatalf(
			"RetireIndexSessions of %s after the lease = %t, %v, want a finished retirement",
			index,
			remaining,
			err,
		)
	}
	requireLeaseEvents(t, stores, mark, session.ID.String(), retireLeaseRecordSize)
}

// TestSearchSessionAuditRetireWriterClockBehindRecoversEarly uses a log hook
// to move the attempt deadline and event times one hour back when cleanup
// logs a failure. A second call recovers the attempt at once. The first call
// then saves its failed event. The test requires one failed event and one
// incomplete event with different event IDs under the first operation.
func TestSearchSessionAuditRetireWriterClockBehindRecoversEarly(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-writer-behind"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	lateContext := withFutureOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(lateContext)
	if err != nil {
		t.Fatalf("read the attempt operation: %v", err)
	}
	hooks := installRetireLogHooks(
		t,
		session.ID.String(),
		map[string]func(){
			retireCleanupFailedMessage: func() {
				key := retireFamilyKey(t, retireAttemptFamily)
				integration.ReplaceRawValue(
					t,
					key,
					shiftedAttemptRecord(
						t,
						retireFamilyValue(t, retireAttemptFamily),
						-retireWriterOffset,
						time.Time{},
					),
				)
				if _, err := rebuilds.RetireIndexSessions(
					integration.WithTestOperation(t, t.Context()),
					index,
					retireSliceLimit,
				); err != nil {
					t.Fatalf("recover the attempt of index %s: %v", index, err)
				}
			},
		},
	)

	_, err = rebuilds.RetireIndexSessions(lateContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	requireChangeEventFailure(t, "RetireIndexSessions of "+index, err)
	failed := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupClose)
	recovered := cleanupFailureEvents(
		t,
		stores,
		mark,
		session.ID,
		searchdomain.CleanupStep(retireResultNotSaved),
	)
	if len(failed) != 1 || len(recovered) != 1 || failed[0].EventID == recovered[0].EventID ||
		failed[0].Context.OperationID != operation.Context.OperationID || recovered[0].Context.OperationID != operation.Context.OperationID {
		t.Fatalf(
			"failed events = %+v, incomplete events = %+v, want 1 of each with different IDs under operation %s",
			failed,
			recovered,
			operation.Context.OperationID,
		)
	}
}

func requireLeaseEvents(
	t *testing.T,
	stores *fdbadapter.Stores,
	mark []byte,
	sessionID string,
	want int,
) {
	t.Helper()
	found := 0
	for _, event := range outboxPlainEvents(t, stores, mark) {
		if event.Error != nil && event.Error.Code == retireResultNotSaved {
			found++
		}
	}
	if found != want {
		t.Fatalf("session %s has %d incomplete events, want %d", sessionID, found, want)
	}
	if invalid := invalidAttemptEvents(t, stores, mark); len(invalid) != 0 {
		t.Fatalf("session %s has invalid attempt events %+v, want 0", sessionID, invalid)
	}
}

// shiftedAttemptRecord moves the deadline and event times of stored by
// offset. A nonzero observed value sets the observation time.
func shiftedAttemptRecord(
	t *testing.T,
	stored []byte,
	offset time.Duration,
	observed time.Time,
) []byte {
	t.Helper()
	fields := map[string]json.RawMessage{}
	event := map[string]json.RawMessage{}
	eventContext := map[string]json.RawMessage{}
	if err := errors.Join(
		json.Unmarshal(stored, &fields),
		json.Unmarshal(fields["event"], &event),
		json.Unmarshal(event["context"], &eventContext),
	); err != nil {
		t.Fatalf("decode the attempt record: %v", err)
	}
	shiftJSONTime(t, fields, "deadline", offset)
	shiftJSONTime(t, event, "occurred_at", offset)
	shiftJSONTime(t, eventContext, "started_at", offset)
	if !observed.IsZero() {
		fields["observed_at"] = encodeAttemptJSON(t, observed)
	}
	event["context"] = encodeAttemptJSON(t, eventContext)
	fields["event"] = encodeAttemptJSON(t, event)
	return encodeAttemptJSON(t, fields)
}

func shiftJSONTime(
	t *testing.T,
	fields map[string]json.RawMessage,
	name string,
	offset time.Duration,
) {
	t.Helper()
	var value time.Time
	if err := json.Unmarshal(fields[name], &value); err != nil {
		t.Fatalf("decode the attempt field %s: %v", name, err)
	}
	fields[name] = encodeAttemptJSON(t, value.Add(offset))
}

func encodeAttemptJSON[Value time.Time | map[string]json.RawMessage](
	t *testing.T,
	value Value,
) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode the attempt value: %v", err)
	}
	return encoded
}
