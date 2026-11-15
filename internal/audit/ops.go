package audit

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// systemOrgID identifies the org for global operator events without a customer
// org. EventContext.OrgID requires a non-nil UUID. Keep this variable private
// to prevent importing packages from changing the org for later events.
var systemOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000005ee")

// SystemOrgID returns the org that global operator commands record against.
func SystemOrgID() uuid.UUID {
	return systemOrgID
}

// Spec declares an operator command's audit requirements. Mutates is true for
// commands that change state. Atomic requires the event and FoundationDB change
// to commit in one transaction. Reads is true for read-only commands that record
// their access.
type Spec struct {
	Verb    string
	Mutates bool `exhaustruct:"optional"`
	Atomic  bool `exhaustruct:"optional"`
	Reads   bool `exhaustruct:"optional"`
	// CreatesAuditInfrastructure defers recording when the outbox table or
	// operator login does not exist, or authentication prevents the initial
	// intent write. The gate retries recording intent and outcome after the
	// command returns. Authentication failures require the command to repair
	// the credentials before recording can succeed. Only seed-roles within
	// this command can repair mismatched credentials. The operator role cannot
	// drop either object; only a superuser can create either absence. A missing
	// login proves that an operator command has not previously recorded an event.
	CreatesAuditInfrastructure bool `exhaustruct:"optional"`
	// Class is the operation class of the definition. ValidateSpec rejects a
	// read class on a definition that mutates.
	Class OperationClass `exhaustruct:"optional"`
	// Targets returns the typed targets for one operation input.
	Targets func(context.Context, json.RawMessage) ([]Target, error) `exhaustruct:"optional"`
	// SafeInput returns the input fields the ledger may store, without
	// credentials or raw request bodies.
	SafeInput func(json.RawMessage) (json.RawMessage, error) `exhaustruct:"optional"`
}

// InfrastructureState reports the audit infrastructure the operator gate needs.
type InfrastructureState struct {
	// OutboxTableExists reports whether public.ops_outbox exists.
	OutboxTableExists bool
	// OperatorLoginExists reports whether tack_audit_operator exists.
	OperatorLoginExists bool
	// OperatorReadsExist reports whether public.ops_read_begin exists and
	// audit_operator can execute it.
	OperatorReadsExist bool `exhaustruct:"optional"`
}

// InfrastructureProbe reports the audit infrastructure state before a command
// that creates it runs.
type InfrastructureProbe func(context.Context) (InfrastructureState, error)

// OutboxWriter durably writes an operator event.
type OutboxWriter interface {
	WriteOutbox(ctx context.Context, event Event) error
}

// IdempotentOutboxWriter writes an event only when its event identity is not
// already present. Reconstructed history uses this contract to resume partial
// runs without duplicating ledger events.
type IdempotentOutboxWriter interface {
	WriteOutboxIfAbsent(ctx context.Context, event Event) (bool, error)
}

// OperatorPrincipal identifies the person or service running an operator
// command.
type OperatorPrincipal struct {
	// ID is the stable identity of the operator.
	ID uuid.UUID
	// Email is the operator email snapshot. A service principal omits it.
	Email string
	// Name is the operator display name snapshot.
	Name string
	// Source identifies the mechanism that resolved the principal.
	Source string
	// Kind identifies the ledger actor kind. The zero value means
	// ActorOperator. The service identity source sets ActorService.
	Kind ActorType `exhaustruct:"optional"`
	// SessionID identifies the agent session that ran the command. The
	// service identity source sets it from the session flag; a human principal
	// leaves it empty.
	SessionID string `exhaustruct:"optional"`
	// OnBehalfOf identifies the accountable human operator when a service
	// principal runs a command for that operator. It is nil when the principal
	// acts on its own account.
	OnBehalfOf *ActProvenance `exhaustruct:"optional"`
}

// ActorType returns the ledger actor kind for this principal.
// A zero Kind returns ActorOperator.
func (p OperatorPrincipal) ActorType() ActorType {
	if p.Kind == "" {
		return ActorOperator
	}

	return p.Kind
}

// OperatorIdentitySource resolves the operator identity for a command.
type OperatorIdentitySource interface {
	Resolve(ctx context.Context) (OperatorPrincipal, error)
}
