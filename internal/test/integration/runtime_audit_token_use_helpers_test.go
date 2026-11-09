package integration_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"

	fdbadapter "goodkind.io/tack/internal/adapters/foundationdb"
	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	// tokenUseNeverFlushInterval stops the buffered queue from flushing
	// during the test.
	tokenUseNeverFlushInterval = time.Hour
	// tokenUseOutboxTimeout is the transaction timeout of the test outbox reader.
	tokenUseOutboxTimeout = 5 * time.Second
	// tokenUseOutboxReadLimit is the most outbox entries one read returns.
	tokenUseOutboxReadLimit = 500
	// apiAccessTargetPrefix is the resource URI prefix for an API token target.
	apiAccessTargetPrefix = "tack:api_token/"
	// actorFixtureLabel is the label datagen stores with each actor token.
	actorFixtureLabel = "qa-datagen"
	// tokenUseProjectionWait limits the wait for the audit consumer to update
	// the token's last use time.
	tokenUseProjectionWait = 30 * time.Second
)

// outboxTokenUsed returns the auth.token_used event for requestID from the
// FoundationDB outbox under the test prefix. It fails the test when the outbox
// does not contain the event.
func outboxTokenUsed(t *testing.T, server *publicServer, requestID string) audit.Event {
	t.Helper()

	database, err := fdbadapter.Open(server.clusterFile, tokenUseOutboxTimeout)
	if err != nil {
		t.Fatalf("fdbadapter.Open failed: %v", err)
	}

	store := fdbadapter.NewOpsOutboxStore(database)

	var mark []byte

	for {
		entries, err := store.ReadOutboxFrom(t.Context(), mark, tokenUseOutboxReadLimit)
		if err != nil {
			t.Fatalf("reading the outbox failed: %v", err)
		}

		if len(entries) == 0 {
			t.Fatalf("the outbox does not contain an auth.token_used event "+
				"for request %s when the response returned", requestID)
		}

		for _, entry := range entries {
			var event audit.Event

			err := json.Unmarshal(entry.Event, &event)
			if err != nil {
				t.Fatalf("decoding an outbox entry failed: %v", err)
			}

			if event.Verb == string(audit.VerbAuthTokenUsed) && event.Context.RequestID == requestID {
				return event
			}
		}

		mark = entries[len(entries)-1].Mark
	}
}

// tokenUsedRows returns the auth.token_used rows for requestID in orgID.
func tokenUsedRows(t *testing.T, reader *audit.Reader, orgID uuid.UUID, requestID string) []audit.Row {
	t.Helper()

	rows, err := reader.Query(t.Context(), audit.QueryFilter{
		OrgID: orgID, Oldest: clock.Now().Add(-time.Hour), Latest: clock.Now().Add(time.Hour),
		Action: string(audit.VerbAuthTokenUsed), ActorID: uuid.Nil, EntityID: uuid.Nil,
		RequestID: requestID, TraceID: "", Limit: 10,
		BeforeTime: time.Time{}, BeforeSeq: 0, NilOrg: false,
	})
	if err != nil {
		t.Fatalf("ledger query failed for request %s: %v", requestID, err)
	}

	return rows
}

// fixtureTokenID returns the ID of the api_tokens row for the server token.
func fixtureTokenID(t *testing.T, server *publicServer) uuid.UUID {
	t.Helper()

	stored, err := postgres.NewTokenRepo(ledgerAdminPool(t)).List(t.Context(), server.userID)
	if err != nil {
		t.Fatalf("listing the tokens of user %s failed: %v", server.userID, err)
	}

	for _, entry := range stored {
		if entry.Label == actorFixtureLabel {
			return entry.ID
		}
	}

	t.Fatalf("user %s does not have a token labeled %s", server.userID, actorFixtureLabel)

	return uuid.Nil
}

// requireLastUsed waits for the audit consumer to set api_tokens.last_used for
// tokenID to usedAt. The caller must send exactly one token use because the
// consumer stores the newest event time.
func requireLastUsed(t *testing.T, tokenID uuid.UUID, usedAt time.Time) {
	t.Helper()

	admin := ledgerAdminPool(t)
	query := readTestSQL(t, "token_use_last_used.sql")
	deadline := clock.Now().Add(tokenUseProjectionWait)

	var lastUsed *time.Time

	for {
		err := admin.QueryRow(t.Context(), query, tokenID).Scan(&lastUsed)
		if err != nil {
			t.Fatalf("reading the last use of token %s failed: %v", tokenID, err)
		}

		if lastUsed != nil && lastUsed.Equal(usedAt) {
			return
		}

		if clock.Now().After(deadline) {
			t.Fatalf("api_tokens.last_used of token %s = %v after %s, want %s",
				tokenID, lastUsed, tokenUseProjectionWait, usedAt)
		}

		time.Sleep(200 * time.Millisecond)
	}
}

// requireTokenUsedRowFields checks the actor, token target, request ID,
// operation ID, and outcome of an auth.token_used row.
func requireTokenUsedRowFields(t *testing.T, row audit.Row, userID, tokenID uuid.UUID, requestID string) {
	t.Helper()

	target := row.Context.Target
	if row.ActorID != userID || row.Context.RequestID != requestID ||
		string(row.Outcome) != string(audit.OutcomeCompleted) ||
		row.Context.OperationID == uuid.Nil || row.Context.APITokenID != tokenID ||
		target == nil || target.ID != tokenID || target.URI != apiAccessTargetPrefix+tokenID.String() {
		t.Fatalf("auth.token_used row = actor %s, request %q, operation %s, outcome %q, "+
			"token %s, target %+v; want actor %s, request %q, an operation ID, "+
			"outcome %q, token %s, target %s%s",
			row.ActorID, row.Context.RequestID, row.Context.OperationID, row.Outcome,
			row.Context.APITokenID, target,
			userID, requestID, audit.OutcomeCompleted, tokenID, apiAccessTargetPrefix, tokenID)
	}
}
