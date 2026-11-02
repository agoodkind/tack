//go:build runtimeaudit

package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/auditrelay"
	"goodkind.io/tack/internal/clock"
	searchdomain "goodkind.io/tack/internal/domain/search"
	"goodkind.io/tack/internal/test/integration"
	"goodkind.io/tack/internal/testenv"
)

const (
	retireAttemptInvalid = "cleanup_attempt_invalid"
	invalidAttemptValue  = "{"
	outcomeRequiredText  = "outcome required"
)

// TestSearchSessionAuditRetireInvalidAttemptRecord pauses FoundationDB before
// the failure event write and replaces the remaining attempt record value with
// invalid JSON. It requires the next RetireIndexSessions call to finish
// retirement and delete the session. It checks for one incomplete event with
// the call's actor and operation ID, the system organization, and only the
// SHA-256 digest of the stored value in its evidence. It then runs the
// production relay and audit consumer and requires one audit.events_dlq row
// containing the stored value.
func TestSearchSessionAuditRetireInvalidAttemptRecord(t *testing.T) {
	stores := sessionStoreStores(t)
	rebuilds := stores.SearchRebuilds(clock.Wall{})
	store := stores.SearchSessions(clock.Wall{}, sessionStoreIdle)
	container := testenv.FoundationDBContainer(testenv.FoundationDB(t))
	index := "node-pages-invalid-attempt"
	session := retiringSession(t, stores, index)
	mark := searchOutboxTail(t, stores)
	gate := installRetirePauseGate(t, container, session.ID.String(), retireCleanupFailedMessage)
	if _, err := rebuilds.RetireIndexSessions(withFutureOperation(t, t.Context()), index, retireSliceLimit); err == nil {
		t.Fatalf("RetireIndexSessions of %s under the failing operation returned no error", index)
	}
	gate.requireFinished(t)
	integration.ReplaceRawValue(t, retireAttemptRawKey(t), []byte(invalidAttemptValue))

	retryContext := integration.WithTestOperation(t, t.Context())
	operation, err := audit.RequireMutationContext(retryContext)
	if err != nil {
		t.Fatalf("read the retry operation: %v", err)
	}
	remaining, err := rebuilds.RetireIndexSessions(retryContext, index, retireSliceLimit)
	if err != nil || remaining {
		t.Fatalf("RetireIndexSessions of %s with an invalid attempt record = %t, %v, want a finished retirement", index, remaining, err)
	}
	if _, err := store.Load(t.Context(), session.ID); !errors.Is(err, searchdomain.ErrSessionNotFound) {
		t.Fatalf("load session %s after retirement = %v, want %v", session.ID, err, searchdomain.ErrSessionNotFound)
	}
	events := cleanupFailureEvents(t, stores, mark, session.ID, searchdomain.CleanupStep(retireAttemptInvalid))
	if len(events) != 1 {
		t.Fatalf("session %s has %d invalid attempt events, want 1", session.ID, len(events))
	}
	event := events[0]
	if event.Actor.ID != integration.TestActorID || event.Context.OperationID != operation.Context.OperationID ||
		event.Outcome != audit.OutcomeIncomplete || event.Context.OrgID != audit.SystemOrgID() || event.Verb != string(audit.VerbSearchSessionUpdate) {
		t.Fatalf("invalid attempt event = %+v, want an incomplete session update by actor %s under operation %s", event, integration.TestActorID, operation.Context.OperationID)
	}
	var evidence struct {
		Digest string `json:"invalid_attempt_sha256"`
		Record []byte `json:"invalid_attempt_record"`
	}
	digest := sha256.Sum256([]byte(invalidAttemptValue))
	if err := json.Unmarshal(event.Extra, &evidence); err != nil || evidence.Digest != hex.EncodeToString(digest[:]) || evidence.Record != nil {
		t.Fatalf("invalid attempt evidence = %s, %v, want only the digest of %q", event.Extra, err, invalidAttemptValue)
	}

	retainedID := retainedAttemptEvent(t, stores, mark, session.ID)
	stack := startRuntimeAuditStack(t)
	startRetireRelay(t, stack)
	letter := stack.waitForEventDeadLetters(t, []uuid.UUID{retainedID})[retainedID]
	if !strings.Contains(letter.Error, outcomeRequiredText) {
		t.Fatalf("dead letter error of event %s = %q, want text %q", retainedID, letter.Error, outcomeRequiredText)
	}
	var retained audit.Event
	if err := json.Unmarshal(letter.Payload, &retained); err != nil {
		t.Fatalf("decode the dead letter of event %s: %v", retainedID, err)
	}
	if err := json.Unmarshal(retained.Extra, &evidence); err != nil || string(evidence.Record) != invalidAttemptValue {
		t.Fatalf("dead letter evidence = %s, %v, want the stored value %q", retained.Extra, err, invalidAttemptValue)
	}
}

func retainedAttemptEvent(t *testing.T, stores *fdbadapter.Stores, mark []byte, sessionID uuid.UUID) uuid.UUID {
	t.Helper()
	entries, err := stores.OpsOutbox.ReadOutboxFrom(t.Context(), mark, searchAuditPageLimit)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	for _, entry := range entries {
		var event audit.Event
		if err := json.Unmarshal(entry.Event, &event); err != nil {
			t.Fatalf("decode outbox entry %s: %v", entry.Event, err)
		}
		if event.Entity.ID == sessionID && event.Outcome == "" {
			return event.EventID
		}
	}
	t.Fatalf("the outbox does not contain the retained attempt event of session %s", sessionID)
	return uuid.Nil
}

func startRetireRelay(t *testing.T, stack runtimeAuditStack) {
	t.Helper()
	opened, err := auditrelay.Open(t.Context(), auditrelay.Config{
		Brokers: []string{testenv.Kafka(t)}, Topic: stack.topic, YugabyteDSN: testenv.Ledger(t),
		FDBClusterFile: testenv.FoundationDB(t), FDBTransactionTimeout: integration.StoreTransactionTimeout,
		PollInterval: 100 * time.Millisecond, BatchSize: 64,
	})
	if err != nil {
		t.Fatalf("open the audit relay: %v", err)
	}
	opened.Relay.Start(t.Context())
	t.Cleanup(opened.Pool.Close)
	t.Cleanup(func() {
		if err := opened.Relay.Close(); err != nil {
			t.Errorf("close the audit relay: %v", err)
		}
	})
}
