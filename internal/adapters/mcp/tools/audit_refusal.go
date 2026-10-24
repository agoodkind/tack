package tools

import (
	"context"
	"errors"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
)

// Typed failure codes that tool calls record in audit events.
const (
	accessDeniedCode     = "access_denied"
	notFoundCode         = "not_found"
	invalidReferenceCode = "invalid_reference"
	invalidArgumentCode  = "invalid_argument"
)

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

// refusalCode returns the audit failure code and message for a refused tool
// call with err. The message is the refusal error text that the tool returns
// to the caller, except for a membership refusal. It reports false when err
// is not a refusal.
func refusalCode(err error) (string, string, bool) {
	var refusal membershipRefusalError
	var reference invalidReferenceError
	switch {
	case errors.As(err, &refusal):
		return accessDeniedCode, accessDeniedMessage, true
	case errors.Is(err, domain.ErrNotFound):
		return notFoundCode, err.Error(), true
	case errors.As(err, &reference):
		return invalidReferenceCode, err.Error(), true
	case errors.Is(err, domain.ErrInvalidArgument):
		return invalidArgumentCode, err.Error(), true
	case errors.Is(err, domain.ErrAlreadyExists):
		return "already_exists", err.Error(), true
	case errors.Is(err, domain.ErrConflict):
		return "conflict", err.Error(), true
	case errors.Is(err, domain.ErrFailedPrecondition):
		return "failed_precondition", err.Error(), true
	default:
		return "", "", false
	}
}

// noteRefusal records the refusal code of err for the request's audit events.
func noteRefusal(ctx context.Context, err error) {
	if code, message, refused := refusalCode(err); refused {
		noteOutcome(ctx, audit.OutcomeRefused, code, message)
	}
}
