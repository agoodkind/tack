package ops

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

// requireAccountableOperator refuses a service principal with no accountable
// operator. A write recorded as a product user records the person accountable
// for it, and a service alone is not a person.
func requireAccountableOperator(ctx context.Context, principal audit.OperatorPrincipal) error {
	if principal.ActorType() != audit.ActorService || principal.OnBehalfOf != nil {
		return nil
	}
	err := errors.New("act-as by --operator-service requires --operator-id and --operator-email for the accountable operator")
	slog.ErrorContext(ctx, "act_as.accountable_operator_missing", slog.String("service", principal.Name), slog.String("err", err.Error()))
	return err
}

// userRowProvenance returns the provenance staged on a row written as a
// product user under grantID. A human principal records its own ID and email.
// A service principal with an accountable operator records that operator's ID
// and email, plus the agent service name, ID, and session.
func userRowProvenance(principal audit.OperatorPrincipal, grantID uuid.UUID, reason string) audit.ActProvenance {
	provenance := audit.ActProvenance{
		OperatorID: principal.ID, OperatorEmail: principal.Email, GrantID: grantID, Reason: reason,
	}
	if principal.OnBehalfOf == nil {
		return provenance
	}
	provenance.OperatorID, provenance.OperatorEmail = principal.OnBehalfOf.OperatorID, principal.OnBehalfOf.OperatorEmail
	provenance.AgentName, provenance.AgentID, provenance.AgentSessionID = principal.Name, principal.ID, principal.SessionID
	return provenance
}

// onBehalfOfWithReason returns a copy of the principal's provenance with
// Reason set to the command's reason, or nil when the principal acts on its
// own account. The principal's own provenance value stays unchanged.
func onBehalfOfWithReason(principal audit.OperatorPrincipal, reason string) *audit.ActProvenance {
	if principal.OnBehalfOf == nil {
		return nil
	}
	provenance := *principal.OnBehalfOf
	provenance.Reason = reason
	return &provenance
}
