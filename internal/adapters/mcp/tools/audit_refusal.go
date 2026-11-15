package tools

import (
	"context"
	"errors"

	"goodkind.io/tack/internal/audit"
)

// Typed failure codes that tool calls record in audit events.
const accessDeniedCode = "access_denied"

// membershipRefusalError wraps the not-found error that a membership check
// returns for a target in an organization that the caller does not belong
// to. The response text matches an absent target. The audit event records
// accessDeniedCode.
type membershipRefusalError struct{ err error }

func (refusal membershipRefusalError) Error() string { return refusal.err.Error() }

func (refusal membershipRefusalError) Unwrap() error { return refusal.err }

// invalidReferenceError wraps the invalid-argument error for a node reference
// that does not match any reference format of its node type.
type invalidReferenceError struct{ err error }

func (reference invalidReferenceError) Error() string { return reference.err.Error() }

func (reference invalidReferenceError) Unwrap() error { return reference.err }

// accessDeniedMessage is the audit failure message of a membership refusal.
// The error text of that refusal matches an absent target.
const accessDeniedMessage = "the caller is not a member of the target organization"

// noteRefusal records membership denials, including masked not-found errors.
func noteRefusal(ctx context.Context, err error) {
	if _, denied := errors.AsType[membershipRefusalError](err); denied {
		noteOutcome(ctx, audit.OutcomeRefused, accessDeniedCode, accessDeniedMessage)
	}
}
