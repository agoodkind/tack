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
	// CreatesAuditInfrastructure defers recording when the outbox table or
	// operator login is absent, or when database authentication prevents the
	// first intent write after both exist. It never skips recording: the gate
	// writes intent and outcome after the infrastructure command returns. The
	// authentication case is safe because the only in-band repair for a
	// credential mismatch is the seed-roles step inside this command, and both
	// rows land after it, so nothing goes unaudited. The operator role can drop
	// neither object. Only a superuser can manufacture either absence, and an
	// absent login proves no operator command has ever recorded.
	CreatesAuditInfrastructure bool `exhaustruct:"optional"`
}

// InfrastructureState reports the audit infrastructure the operator gate needs.
type InfrastructureState struct {
	// OutboxTableExists reports whether public.ops_outbox exists.
	OutboxTableExists bool
	// OperatorLoginExists reports whether tack_audit_operator exists.
	OperatorLoginExists bool
}

// InfrastructureProbe reports the audit infrastructure state before a command
// that creates it runs.
type InfrastructureProbe func(context.Context) (InfrastructureState, error)

// OutboxWriter durably writes an operator event.
type OutboxWriter interface {
	WriteOutbox(ctx context.Context, event Event) error
}

// IdempotentOutboxWriter writes an event only when its event identity is not
// already present. Reconstructed history uses this narrow contract so an
// operator can resume a partial run without duplicating ledger history.
type IdempotentOutboxWriter interface {
	WriteOutboxIfAbsent(ctx context.Context, event Event) (bool, error)
}

// OperatorPrincipal identifies the person or service running an operator
// command.
type OperatorPrincipal struct {
	// ID is the stable identity of the operator.
	ID uuid.UUID
	// Email is the operator email snapshot. A service principal has none.
	Email string
	// Name is the operator display name snapshot.
	Name string
	// Source identifies the mechanism that resolved the principal.
	Source string
	// Kind names the ledger actor kind. The zero value means ActorOperator,
	// which keeps the existing human sources unchanged; the service identity
	// source sets ActorService so a daemon is never recorded as a human.
	Kind ActorType `exhaustruct:"optional"`
}

// OperatorIdentitySource resolves the operator identity for a command.
type OperatorIdentitySource interface {
	Resolve(ctx context.Context) (OperatorPrincipal, error)
}
