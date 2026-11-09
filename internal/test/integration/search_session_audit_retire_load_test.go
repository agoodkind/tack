//go:build runtimeaudit

package integration_test

import (
	"bytes"
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
	retireLoadDeadline     = time.Minute
	retireLoadSlices       = 2
	retireLoadDecodeText   = "decode search session value"
	retireLoadBindingText  = "binding is invalid"
	retireLoadBindingField = "binding"
	retireLoadBindingValue = `"zz"`
)

// TestSearchSessionAuditRetireCorruptHeaderRecorded calls
// RetireIndexSessions twice for each of two corrupt headers on a retiring
// index. When the header contains invalid JSON, each event uses the system
// organization and omits the owner. When only the binding field has an
// invalid value, the header decodes as JSON and each event uses the stored
// organization and owner. Each call reports remaining work, returns the
// cause without audit.ErrEventNotRecorded, and adds one
// session_delete_failed event. Each event records a failed delete, omits
// the raw cause text, and has a distinct event ID. Each call leaves the
// header unchanged.
func TestSearchSessionAuditRetireCorruptHeaderRecorded(t *testing.T) {
	cases := []struct {
		index       string
		header      func(*testing.T, []byte) []byte
		cause       string
		storedScope bool
	}{
		{
			index:       "node-pages-corrupt-header",
			header:      func(*testing.T, []byte) []byte { return []byte(invalidHeaderValue) },
			cause:       retireLoadDecodeText,
			storedScope: false,
		},
		{
			index:       "node-pages-invalid-binding",
			header:      invalidBindingHeader,
			cause:       retireLoadBindingText,
			storedScope: true,
		},
	}
	for _, current := range cases {
		t.Run(
			current.index,
			func(t *testing.T) {
				stores := sessionStoreStores(t)
				rebuilds := stores.SearchRebuilds(clock.Wall{})
				beginRetiringRebuild(t, stores, current.index)
				session := retiringSession(t, stores, current.index)
				header := current.header(t, retireFamilyValue(t, sessionHeaderFamily))
				integration.ReplaceRawValue(t, retireFamilyKey(t, sessionHeaderFamily), header)
				mark := searchOutboxTail(t, stores)

				for slice := 1; slice <= retireLoadSlices; slice++ {
					sliceContext, cancel := context.WithTimeout(
						integration.WithTestOperation(t, t.Context()),
						retireLoadDeadline,
					)
					remaining, err := rebuilds.RetireIndexSessions(
						sliceContext,
						current.index,
						retireSliceLimit,
					)
					cancel()
					if !remaining || err == nil || errors.Is(err, audit.ErrEventNotRecorded) ||
						!strings.Contains(err.Error(), current.cause) {
						t.Fatalf(
							"RetireIndexSessions of %s with a corrupt header = %t, %v, want remaining work and text %q without %v",
							current.index,
							remaining,
							err,
							current.cause,
							audit.ErrEventNotRecorded,
						)
					}
					if events := cleanupFailureEvents(
						t,
						stores,
						mark,
						session.ID,
						searchdomain.CleanupDelete,
					); len(events) != slice {
						t.Fatalf(
							"session %s has %d %s events after slice %d, want %d",
							session.ID,
							len(events),
							searchdomain.CleanupDelete,
							slice,
							slice,
						)
					}
				}
				organization, owner := audit.SystemOrgID(), uuid.Nil
				if current.storedScope {
					organization, owner = session.AuthorityID, session.PrincipalID
				}
				events := cleanupFailureEvents(
					t,
					stores,
					mark,
					session.ID,
					searchdomain.CleanupDelete,
				)
				requireLoadFailureEvents(t, events, organization, owner, current.cause)
				if stored := retireFamilyValue(
					t,
					sessionHeaderFamily,
				); !bytes.Equal(stored, header) {
					t.Fatalf(
						"the header of session %s = %q after retirement, want the unchanged value %q",
						session.ID,
						stored,
						header,
					)
				}
			},
		)
	}
}

func invalidBindingHeader(t *testing.T, stored []byte) []byte {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(stored, &fields); err != nil {
		t.Fatalf("decode the stored session header: %v", err)
	}
	fields[retireLoadBindingField] = json.RawMessage(retireLoadBindingValue)
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("encode the session header with an invalid binding: %v", err)
	}
	return encoded
}

func requireLoadFailureEvents(
	t *testing.T,
	events []audit.Event,
	organization uuid.UUID,
	owner uuid.UUID,
	cause string,
) {
	t.Helper()
	eventIDs := map[uuid.UUID]bool{}
	for _, event := range events {
		eventIDs[event.EventID] = true
		if event.Verb != string(audit.VerbSearchSessionDelete) ||
			event.Outcome != audit.OutcomeFailed ||
			event.Context.OrgID != organization ||
			event.Context.Target == nil ||
			event.Context.Target.OwnerID != owner {
			t.Fatalf(
				"cleanup failure event = %+v, target %+v, want a failed delete in organization %s with owner %s",
				event,
				event.Context.Target,
				organization,
				owner,
			)
		}
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode event %s: %v", event.EventID, err)
		}
		if strings.Contains(string(encoded), cause) {
			t.Fatalf("event %s contains raw error text %q: %s", event.EventID, cause, encoded)
		}
	}
	if len(eventIDs) != retireLoadSlices {
		t.Fatalf(
			"the cleanup failure events have %d distinct event IDs, want %d",
			len(eventIDs),
			retireLoadSlices,
		)
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
	integration.ReplaceRawValue(
		t,
		retireFamilyKey(t, sessionHeaderFamily),
		[]byte(invalidHeaderValue),
	)
	mark := searchOutboxTail(t, stores)
	hooks := installRetirePauseGate(t, container, session.ID.String(), retireCleanupFailedMessage)

	sliceContext, cancel := context.WithTimeout(
		integration.WithTestOperation(t, t.Context()),
		retireLoadDeadline,
	)
	defer cancel()
	remaining, err := rebuilds.RetireIndexSessions(sliceContext, index, retireSliceLimit)
	hooks.requireFinished(t)
	if !remaining || !errors.Is(err, audit.ErrEventNotRecorded) {
		t.Fatalf(
			"RetireIndexSessions of %s with a lost failure event = %t, %v, want remaining work and %v",
			index,
			remaining,
			err,
			audit.ErrEventNotRecorded,
		)
	}
	for _, want := range []string{
		retireLoadDecodeText,
		cleanupRecordingFailure,
		transactionTimedOutText,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RetireIndexSessions of %s error = %v, want text %q", index, err, want)
		}
	}
	if events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupDelete); len(
		events,
	) != 0 {
		t.Fatalf(
			"session %s has %d %s events after a lost write, want 0",
			session.ID,
			len(events),
			searchdomain.CleanupDelete,
		)
	}
}
