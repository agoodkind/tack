package audit

import (
	"context"

	"github.com/google/uuid"
)

// systemOrgID is the org that global operator commands record against.
// EventContext.OrgID is mandatory and the reader rejects the nil UUID, so a
// command with no customer org still needs a real org to record under.
//
// It is unexported deliberately. As an exported variable, any importing
// package could reassign it, which would move later operator events onto a
// different hash chain and change what the stored rows mean, silently.
var systemOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000005ee")

// SystemOrgID returns the org that global operator commands record against.
func SystemOrgID() uuid.UUID {
	return systemOrgID
}

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

// IdempotentOutboxWriter writes an event only when its event identity is not
// already present. Reconstructed history uses this narrow contract so an
// operator can resume a partial run without duplicating ledger history.
type IdempotentOutboxWriter interface {
	WriteOutboxIfAbsent(ctx context.Context, event Event) (bool, error)
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
