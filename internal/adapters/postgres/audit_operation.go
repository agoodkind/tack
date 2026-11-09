// Package postgres opens YugabyteDB connection pools and stores and reads Tack's
// users, api_tokens, and org_members auth tables.
// Each repository change method writes audit change events to the operator outbox
// in the same transaction as the record change.
package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	userTargetPrefix = "tack:user/"
	memberRelation   = "org_member"
)

// changeOperation reads the audit mutation operation from ctx and sets its verb
// and organization ID. It returns an error wrapping
// audit.ErrMissingMutationContext when ctx lacks a valid operation.
func changeOperation(ctx context.Context, verb audit.Verb, orgID uuid.UUID) (audit.Event, error) {
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "audit.change.context_missing", slog.String("verb", string(verb)), slog.String("err", err.Error()))
		return audit.Event{}, fmt.Errorf("record %s in org %s: %w", verb, orgID, err)
	}
	operation.Verb = string(verb)
	operation.Context.OrgID = orgID
	return operation, nil
}

// UserTarget returns a resource audit target for userID.
//
// The target uses userID as its ID and appends its string form to
// userTargetPrefix for the URI. The other target fields are empty or
// zero UUIDs.
//
//	target := UserTarget(userID)
func UserTarget(userID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: "",
		URI: userTargetPrefix + userID.String(), RelationType: "",
		ID: userID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// TokenTarget returns a resource audit target for tokenID.
//
// The target uses tokenID as its ID and label as its name. Its URI
// appends the string form of tokenID to audit.APIAccessURIPrefix.
//
//	target := TokenTarget(tokenID, label)
func TokenTarget(tokenID uuid.UUID, label string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindResource, NodeType: "", Reference: "", Name: label,
		URI: audit.APIAccessURIPrefix + tokenID.String(), RelationType: "",
		ID: tokenID, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// MemberTarget returns a relationship audit target from orgID to userID.
//
// The target uses memberID as the membership row ID. Failure events can
// use [uuid.Nil] for memberID when the row does not exist. The target
// uses memberRelation as its relationship type.
//
//	target := MemberTarget(memberID, orgID, userID)
func MemberTarget(memberID, orgID, userID uuid.UUID) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindRelationship, NodeType: "", Reference: "", Name: "",
		URI: "", RelationType: memberRelation,
		ID: memberID, SourceID: orgID, TargetID: userID,
	}
}
