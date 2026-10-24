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

// refusalCode returns the audit failure code and message for a refused tool
// call with err. It reports false when err is not a refusal.
func refusalCode(err error) (string, string, bool) {
	var refusal membershipRefusalError
	var reference invalidReferenceError
	switch {
	case errors.As(err, &refusal):
		return accessDeniedCode, "the caller is not a member of the target organization", true
	case errors.Is(err, domain.ErrNotFound):
		return notFoundCode, "the requested target does not exist", true
	case errors.As(err, &reference):
		return invalidReferenceCode, "the request contains an invalid node reference", true
	case errors.Is(err, domain.ErrInvalidArgument):
		return invalidArgumentCode, "the request contains an invalid argument", true
	case errors.Is(err, domain.ErrAlreadyExists):
		return "already_exists", "the target already exists", true
	case errors.Is(err, domain.ErrConflict):
		return "conflict", "the request conflicts with the current target", true
	case errors.Is(err, domain.ErrFailedPrecondition):
		return "failed_precondition", "the target state does not allow the request", true
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
