package ops

import "goodkind.io/tack/internal/audit"

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
