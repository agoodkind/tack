package audit

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// systemOrgID identifies the organization for global operator commands.
// EventContext.OrgID is mandatory, and the reader rejects the nil UUID. A
// command without a customer organization still records under a non-nil ID.
//
// The unexported variable prevents importing packages from reassigning it.
// Reassignment would select a different hash chain for later operator events
// and silently change the organization associated with those records.
var systemOrgID = uuid.MustParse("00000000-0000-0000-0000-0000000005ee")

// SystemOrgID returns the organization for global operator commands.
//
// The returned UUID identifies the reserved organization for commands without
// a customer organization. Importing packages cannot reassign the stored ID.
//
//   - Global command events use this ID as their organization.
//   - Repeated calls return the same UUID.
func SystemOrgID() uuid.UUID {
	return systemOrgID
}

// Spec declares the audit behavior of an operator command. Mutates marks a
// command that changes state. Atomic declares that a FoundationDB transaction
// commits the event and the change together. Reads marks a command that only
// reads and records access.
type Spec struct {
	Verb    string
	Mutates bool `exhaustruct:"optional"`
	Atomic  bool `exhaustruct:"optional"`
	Reads   bool `exhaustruct:"optional"`
	// CreatesAuditInfrastructure permits deferred recording when the outbox
	// table or operator login does not exist, or authentication prevents the
	// first intent write after both exist. The gate then attempts to write the
	// intent and outcome after the command returns. A later write can fail.
	//
	// The seed-roles step repairs credential mismatches during infrastructure
	// creation. The operator role cannot drop the table or login. Elevated
	// privileges can remove them, and their absence does not prove that earlier
	// commands never recorded.
	//
	// The audit completeness contract requires durable intent before external
	// effects. Deferred recording does not satisfy that ordering requirement.
	CreatesAuditInfrastructure bool `exhaustruct:"optional"`
	// Class specifies the definition's operation class. ValidateSpec rejects a
	// read class when the definition mutates.
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

// InfrastructureProbe reports the audit infrastructure state before the
// command that creates the infrastructure runs.
type InfrastructureProbe func(context.Context) (InfrastructureState, error)

// OutboxWriter durably writes an operator event.
type OutboxWriter interface {
	WriteOutbox(ctx context.Context, event Event) error
}

// IdempotentOutboxWriter writes an event only when its identity does not
// already exist. Reconstructed history uses this contract to resume partial
// runs without duplicating ledger history.
type IdempotentOutboxWriter interface {
	WriteOutboxIfAbsent(ctx context.Context, event Event) (bool, error)
}

// OperatorPrincipal identifies the person or service running an operator
// command.
type OperatorPrincipal struct {
	// ID is the stable identity of the operator.
	ID uuid.UUID
	// Email records the operator email snapshot. A service principal does not
	// provide an email.
	Email string
	// Name is the operator display name snapshot.
	Name string
	// Source identifies the mechanism that resolved the principal.
	Source string
	// Kind specifies the ledger actor kind. The zero value selects
	// ActorOperator for existing human identity sources. The service identity
	// source sets ActorService to distinguish a daemon from a human.
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

// ActorType returns the ledger actor kind for the principal.
//
// A zero Kind selects ActorOperator. Event builders must use this method
// instead of hardcoding a human kind for a service run.
//
//   - A human principal with zero Kind records as ActorOperator.
//   - A service principal with ActorService records as ActorService.
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
