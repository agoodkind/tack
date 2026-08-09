package audit

import (
	"context"

	"github.com/google/uuid"
)

// SystemOrgID is the org that global operator commands record against.
// EventContext.OrgID is mandatory and the reader rejects uuid.Nil, so
// commands with no customer org still need a real org to record under.
var SystemOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000005ee")

// Spec is the audit declaration every operator command carries. Mutates marks
// a command that changes state.
// Atomic marks a FoundationDB command whose event commits inside the same
// transaction as the change, so the two can never disagree. Reads marks a
// command that only reads and records the access.
type Spec struct {
	Verb    string
	Mutates bool `exhaustruct:"optional"`
	Atomic  bool `exhaustruct:"optional"`
	Reads   bool `exhaustruct:"optional"`
}

// OutboxWriter durably writes an operator event before its command proceeds.
type OutboxWriter interface {
	WriteOutbox(ctx context.Context, event Event) error
}

// OperatorPrincipal identifies the person running an operator command.
type OperatorPrincipal struct {
	// ID is the stable identity of the operator.
	ID uuid.UUID
	// Email is the operator email snapshot.
	Email string
	// Name is the operator display name snapshot.
	Name string
	// Source identifies the mechanism that resolved the principal.
	Source string
}

// OperatorIdentitySource resolves the operator identity for a command.
type OperatorIdentitySource interface {
	Resolve(ctx context.Context) (OperatorPrincipal, error)
}
