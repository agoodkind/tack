package ops

import (
	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

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
