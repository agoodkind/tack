package ops

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/domain/user"
)

const (
	authTokenCreateKeyPrefix = "tack-472-token-create:"
	authTokenRevokeKeyPrefix = "tack-472-token-revoke:"
	authTokenEntityType      = "api_token"
	// authTokenRecordTimeout bounds the ledger write, which runs detached from
	// the command's cancellation: an interrupted command must still finish
	// recording a credential it already issued.
	authTokenRecordTimeout = 30 * time.Second
)

// authTokenAuditNamespace derives one event identity per issue or revocation,
// so a rerun that finds the row already recorded writes nothing new.
var authTokenAuditNamespace = uuid.MustParse("9c1f6a3e-2b4d-5e7f-8a90-1b2c3d4e5f60")

// authTokenOrg names the org a token event belongs to: the user's sole org
// when membership admits exactly one, and the system org otherwise, which is
// the honest answer when nothing singles one out. The same rule stamps auth
// events (TACK-461), so a token's issue and its later uses land in one org.
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

// authTokenEvent builds the product-side row for one issue or revocation. The
// operator is the actor, the token is the entity, and the user it belongs to
// is the entity's identifier, so a query by user, by token, or by operator
// finds it.
func authTokenEvent(
	verb audit.Verb,
	keyPrefix string,
	principal audit.OperatorPrincipal,
	issued *token.Token,
	holder *user.User,
	orgID uuid.UUID,
	occurredAt time.Time,
) audit.Event {
	key := keyPrefix + issued.ID.String()
	return audit.Event{
		Verb:    string(verb),
		EventID: uuid.NewSHA1(authTokenAuditNamespace, []byte(key)),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID,
			Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{
			Type: authTokenEntityType, NodeType: "", ID: issued.ID,
			Identifier: holder.Email, Name: issued.Label,
		},
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: audit.OutcomeOK, Error: nil,
		IdempotencyKey: key, OccurredAt: occurredAt.UTC(), Extra: nil,
	}
}

// recordAuthTokenEvent writes one token event through the outbox on its own
// deadline, detached from the command's cancellation.
func recordAuthTokenEvent(ctx context.Context, outbox audit.OutboxWriter, event audit.Event) error {
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authTokenRecordTimeout)
	defer cancel()
	if err := outbox.WriteOutbox(recordCtx, event); err != nil {
		slog.ErrorContext(ctx, "auth.token.record_failed",
			slog.String("verb", event.Verb),
			slog.String("token_id", event.Entity.ID.String()),
			slog.String("err", err.Error()))
		return fmt.Errorf("record %s for token %s: %w", event.Verb, event.Entity.ID, err)
	}
	return nil
}
