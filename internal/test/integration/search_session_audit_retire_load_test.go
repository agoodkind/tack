//go:build runtimeaudit

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireLoadDeadline   = time.Minute
	retireLoadSlices     = 2
	retireLoadDecodeText = "decode search session value"
)

// TestSearchSessionAuditRetireCorruptHeaderRecorded replaces the session
// header on a retiring index with invalid JSON and calls RetireIndexSessions
// twice. Each call must report remaining work, return the decode error
// without audit.ErrEventNotRecorded, and add one session_delete_failed
// event. Each event must record a failed delete in the system organization,
// omit an owner and the raw decode error, and have a distinct event ID.
// The header must stay unchanged, and the replacement must stay unfinished.
func TestSearchSessionAuditRetireCorruptHeaderRecorded(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	index := "node-pages-corrupt-header"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	integration.ReplaceRawValue(t, retireFamilyKey(t, sessionHeaderFamily), []byte(invalidHeaderValue))
	mark := searchOutboxTail(t, stores)

	for slice := 1; slice <= retireLoadSlices; slice++ {
		sliceContext, cancel := context.WithTimeout(integration.WithTestOperation(t, t.Context()), retireLoadDeadline)
		remaining, err := rebuilds.RetireIndexSessions(sliceContext, index, retireSliceLimit)
		cancel()
		if !remaining || err == nil || errors.Is(err, audit.ErrEventNotRecorded) || !strings.Contains(err.Error(), retireLoadDecodeText) {
			t.Fatalf("RetireIndexSessions of %s with a corrupt header = %t, %v, want remaining work and text %q without %v",
				index, remaining, err, retireLoadDecodeText, audit.ErrEventNotRecorded)
		}
		if events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupDelete); len(events) != slice {
			t.Fatalf("session %s has %d %s events after slice %d, want %d", session.ID, len(events), searchdomain.CleanupDelete, slice, slice)
		}
	}
	eventIDs := map[uuid.UUID]bool{}
	for _, event := range cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupDelete) {
		eventIDs[event.EventID] = true
		if event.Verb != string(audit.VerbSearchSessionDelete) || event.Outcome != audit.OutcomeFailed || event.Context.OrgID != audit.SystemOrgID() ||
			event.Context.Target == nil || event.Context.Target.OwnerID != uuid.Nil {
			t.Fatalf("cleanup failure event = %+v, target %+v, want a failed delete in the system organization without an owner", event, event.Context.Target)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode event %s: %v", event.EventID, err)
		}
		if strings.Contains(string(encoded), retireLoadDecodeText) {
			t.Fatalf("event %s contains raw error text %q: %s", event.EventID, retireLoadDecodeText, encoded)
		}
	}
	if len(eventIDs) != retireLoadSlices {
		t.Fatalf("session %s has %d distinct event IDs, want %d", session.ID, len(eventIDs), retireLoadSlices)
	}
	if stored := retireFamilyValue(t, sessionHeaderFamily); string(stored) != invalidHeaderValue {
		t.Fatalf("the header of session %s = %q after retirement, want the unchanged value %q", session.ID, stored, invalidHeaderValue)
	}
	if _, found, err := rebuilds.CurrentRebuild(t.Context()); err != nil || !found {
		t.Fatalf("the replacement of index %s after a corrupt header: found %t, %v, want it unfinished", index, found, err)
	}
}

// TestSearchSessionAuditRetireCorruptHeaderRecordingLoss replaces the session
// header on a retiring index with invalid JSON. It pauses FoundationDB at the
// cleanup failure log and resumes FoundationDB at the audit failure log.
// RetireIndexSessions must report remaining work and return an error that
// matches audit.ErrEventNotRecorded and contains the decode error, the
// recording error, and FoundationDB error 1031. The outbox must not contain
// a session_delete_failed event for the session.
func TestSearchSessionAuditRetireCorruptHeaderRecordingLoss(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-corrupt-header-loss"
	beginRetiringRebuild(t, stores, index)
	session := retiringSession(t, stores, index)
	integration.ReplaceRawValue(t, retireFamilyKey(t, sessionHeaderFamily), []byte(invalidHeaderValue))
	mark := searchOutboxTail(t, stores)
	hooks := installRetirePauseGate(t, container, session.ID.String(), retireCleanupFailedMessage)

	sliceContext, cancel := context.WithTimeout(integration.WithTestOperation(t, t.Context()), retireLoadDeadline)
	defer cancel()
	remaining, err := rebuilds.RetireIndexSessions(sliceContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if !remaining || !errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf("RetireIndexSessions of %s with a lost failure event = %t, %v, want remaining work and %v", index, remaining, err, audit.ErrEventNotRecorded)
	}
	for _, want := range []string{retireLoadDecodeText, cleanupRecordingFailure, transactionTimedOutText} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RetireIndexSessions of %s error = %v, want text %q", index, err, want)
		}
	}
	if events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupDelete); len(events) != 0 {
		t.Fatalf("session %s has %d %s events after a lost write, want 0", session.ID, len(events), searchdomain.CleanupDelete)
	}
}
