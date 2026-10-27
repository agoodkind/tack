package auth

import (
	"context"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	// apiAccessURIPrefix starts the resource URI of an API token target.
	apiAccessURIPrefix = "tack:api_token/"
	// userTargetPrefix starts the resource URI of a user target.
	userTargetPrefix = "tack:user/"
)

// tokenTarget returns the resource target of the API token tokenID.
func tokenTarget(tokenID uuid.UUID, label string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: label,
		URI: apiAccessURIPrefix + tokenID.String(), RelationType: "", ID: tokenID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// userTarget returns the resource target of the user userID.
func userTarget(userID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "",
		URI: userTargetPrefix + userID.String(), RelationType: "", ID: userID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// tokenUsedEvent returns the format 2 auth.token_used event for an accepted
// bearer. The event uses the request operation ID from audit.WithOpID, or a
// new UUIDv7 when ctx does not have one. It uses the system organization when
// orgID is [uuid.Nil]. The event records a completed database change result
// because the audit-consumer writes the token's last use from
// Context.APITokenID.
func tokenUsedEvent(
	ctx context.Context,
	startedAt time.Time,
	actor audit.Actor,
	orgID uuid.UUID,
	target audit.Target,
	tokenID uuid.UUID,
) audit.Event {
	operationID, found := audit.OpIDFromContext(ctx)
	if !found || operationID == uuid.Nil {
		operationID = uuid.Must(uuid.NewV7())
	}
	if orgID == uuid.Nil {
		orgID = audit.SystemOrgID()
	}
	completedAt := clock.Now().UTC()
	eventTarget := target
	return audit.Event{
		Verb: string(audit.VerbAuthTokenUsed), EventID: uuid.Must(uuid.NewV7()), Actor: actor,
		Entity: audit.Entity{Type: target.Kind, NodeType: "", ID: target.ID, Identifier: "", Name: target.Name},
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceMCP, Tool: "", RPC: "", Reason: "",
			APITokenID:         tokenID,
			AuditFormatVersion: audit.AuditFormatComplete, OperationClass: audit.OperationDatabaseChange,
			Phase: audit.PhaseResult, OperationID: operationID, StartedAt: startedAt.UTC(), CompletedAt: completedAt,
			Target: &eventTarget,
		},
		Delta: nil, Outcome: audit.OutcomeCompleted, Error: nil, IdempotencyKey: "", OccurredAt: completedAt, Extra: nil,
	}
}
