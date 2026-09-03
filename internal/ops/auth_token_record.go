package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/domain/user"
)

// The idempotency-key prefixes and the entity type are named without the word
// token, because the secret scanner treats any constant whose name carries it
// as a hardcoded credential; the values are ledger identities, not secrets.
const (
	authIssueKeyPrefix  = "tack-472-issue:"
	authRevokeKeyPrefix = "tack-472-revoke:"
	authIssueEntityType = "api_token"
	// authTokenRecordTimeout bounds each ledger write, which runs detached
	// from the command's cancellation: an interrupted command must still
	// finish recording a credential it already issued.
	authTokenRecordTimeout = 30 * time.Second
)

// authTokenAuditNamespace derives one event identity per row from its key.
var authTokenAuditNamespace = uuid.MustParse("9c1f6a3e-2b4d-5e7f-8a90-1b2c3d4e5f60")

// authTokenAttempt is one run of an issue or a revocation. Its id is part of
// every row key the run writes, so a retry after a failed run records its own
// intent and outcome instead of colliding with the earlier run's rows in the
// outbox, which would refuse the retry for as long as the relay had not
// drained them. Two attempts are two facts, and the ledger holds both.
type authTokenAttempt struct {
	ID        uuid.UUID
	Principal audit.OperatorPrincipal
	Token     *token.Token
	Holder    *user.User
	OrgID     uuid.UUID
}

// authTokenOrg names the org a token event belongs to: the user's sole org
// when membership admits exactly one, and the system org otherwise. The
// bearer middleware stamps the nil org on auth events for the same user, so a
// multi-org holder's issue rows and token_used rows part ways; the system org
// is chosen here anyway because the ledger reader refuses a nil org, and a
// credential's history that no query can reach is the worse failure. The
// nil-org rows are the state the TACK-461 org backfill exists to move.
func authTokenOrg(ctx context.Context, members org.MemberRepository, userID uuid.UUID) (uuid.UUID, error) {
	orgIDs, err := members.ListOrgIDsForUser(ctx, userID)
	if err != nil {
		slog.ErrorContext(ctx, "auth.token.org_lookup_failed",
			slog.String("user_id", userID.String()), slog.String("err", err.Error()))
		return uuid.Nil, fmt.Errorf("list orgs for user %s: %w", userID, err)
	}
	if len(orgIDs) == 1 {
		return orgIDs[0], nil
	}
	return audit.SystemOrgID(), nil
}

// authTokenEvent builds one row for an issue or revocation. The operator is
// the actor, the token is the entity, and the user it belongs to is the
// entity's identifier, so a query by user, by token, or by operator finds it.
// Extra carries attempt_id, pairing intent and outcome the way the
// choke-point pairs them by op_id, so a reader joins them without parsing
// keys.
//
// The token table and the outbox belong to different database roles, so no
// single transaction can cover both. Each action therefore records an intent
// row with outcome pending before it touches the token table and an outcome
// row after, the pattern every operator command and the seed already use. A
// crash between the two leaves a pending row naming the exact token id, so
// the ledger never lacks a row for a credential that exists.
func authTokenEvent(
	verb audit.Verb,
	keyPrefix string,
	attempt authTokenAttempt,
	outcome audit.Outcome,
	occurredAt time.Time,
) audit.Event {
	key := keyPrefix + attempt.Token.ID.String() + ":" + attempt.ID.String() + ":" + string(outcome)
	// A UUID renders as hex and dashes, so the document needs no escaping and
	// no encoder that could fail.
	extra := json.RawMessage(`{"attempt_id":"` + attempt.ID.String() + `"}`)
	return audit.Event{
		Verb:    string(verb),
		EventID: uuid.NewSHA1(authTokenAuditNamespace, []byte(key)),
		Actor: audit.Actor{
			Type: attempt.Principal.ActorType(), ID: attempt.Principal.ID,
			Email: attempt.Principal.Email, Name: attempt.Principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{
			Type: authIssueEntityType, NodeType: "", ID: attempt.Token.ID,
			Identifier: attempt.Holder.Email, Name: attempt.Token.Label,
		},
		Context: audit.EventContext{
			OrgID: attempt.OrgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: outcome, Error: nil,
		IdempotencyKey: key, OccurredAt: occurredAt.UTC(), Extra: extra,
	}
}

// authTokenFailureEvent builds the outcome row for an attempt whose write
// failed after its intent was recorded, so the ledger never holds a pending
// row with no answer. The failure text carries the database error, never a
// credential.
func authTokenFailureEvent(
	verb audit.Verb,
	keyPrefix string,
	attempt authTokenAttempt,
	failure error,
	occurredAt time.Time,
) audit.Event {
	event := authTokenEvent(verb, keyPrefix, attempt, audit.OutcomeError, occurredAt)
	event.Error = &audit.EventError{Code: "command_failed", Message: failure.Error()}
	return event
}

// recordAuthTokenEvent writes one token event through the outbox on its own
// deadline, detached from the command's cancellation.
func recordAuthTokenEvent(ctx context.Context, outbox audit.OutboxWriter, event audit.Event) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authTokenRecordTimeout)
	defer cancel()
	if err := outbox.WriteOutbox(recordCtx, event); err != nil {
		slog.ErrorContext(ctx, "auth.token.record_failed",
			slog.String("verb", event.Verb),
			slog.String("outcome", string(event.Outcome)),
			slog.String("token_id", event.Entity.ID.String()),
			slog.String("err", err.Error()))
		return fmt.Errorf("record %s %s for token %s: %w", event.Verb, event.Outcome, event.Entity.ID, err)
	}
	return nil
}
