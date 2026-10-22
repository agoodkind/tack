// Package postgres opens YugabyteDB connection pools and stores and reads Tack's
// users, api_tokens, and org_members auth tables.
// Each repository change method writes audit change events to the operator outbox
// in the same transaction as the record change.
package postgres

import (
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

const (
	userTargetPrefix   = "tack:user/"
	apiAccessURIPrefix = "tack:api_token/"
	memberRelation     = "org_member"
)

// NewChangeOperation builds the operation event for a repository change using
// the supplied verb, actor, orgID, and source. It assigns a new UUIDv7
// operation ID and the current UTC start time.
func NewChangeOperation(verb audit.Verb, actor audit.Actor, orgID uuid.UUID, source audit.Source) audit.Event {
	startedAt := clock.Now().UTC()
	return audit.Event{
		Verb: string(verb), EventID: uuid.Nil, Actor: actor,
		Entity: audit.Entity{Type: "", NodeType: "", ID: uuid.Nil, Identifier: "", Name: ""},
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: source, Tool: "", RPC: "", Reason: "",
			OperationID: uuid.Must(uuid.NewV7()), StartedAt: startedAt,
		},
		Delta: nil, Outcome: "", Error: nil, IdempotencyKey: "", OccurredAt: startedAt, Extra: nil,
	}
}

// OperatorActor returns an audit actor with the principal's identity and
// session ID.
func OperatorActor(principal audit.OperatorPrincipal) audit.Actor {
	return audit.Actor{
		Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
		SessionID: principal.SessionID, IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
	}
}

// UserTarget returns a resource audit target for userID.
func UserTarget(userID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "",
		URI: userTargetPrefix + userID.String(), RelationType: "",
		ID: userID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// TokenTarget returns a resource audit target for tokenID with label as its
// name.
func TokenTarget(tokenID uuid.UUID, label string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: label,
		URI: apiAccessURIPrefix + tokenID.String(), RelationType: "",
		ID: tokenID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// MemberTarget returns a relationship audit target from orgID to userID, using
// memberID as the membership row ID. Failure events can use [uuid.Nil] for
// memberID when the row does not exist.
func MemberTarget(memberID, orgID, userID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "",
		URI: "", RelationType: memberRelation,
		ID: memberID, SourceID: orgID, TargetID: userID,
	}
}
