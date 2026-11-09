package audit

import (
	"context"

	"github.com/google/uuid"
)

type opIDKey struct{}

type operatorPrincipalKey struct{}

// WithOpID attaches the operation ID that correlates a command's intent and
// outcome rows.
//
// The command writes intent before execution and outcome afterward. The
// returned context stores the UUID under the operator operation key.
//
//	commandContext := WithOpID(ctx, operationID)
//	attachedID, attached := OpIDFromContext(commandContext)
func WithOpID(ctx context.Context, opID uuid.UUID) context.Context {
	return context.WithValue(ctx, opIDKey{}, opID)
}

// OpIDFromContext returns the operation ID that [WithOpID] attached to ctx.
//
// The boolean reports whether the context contains a UUID under the operator
// operation key. It does not validate that UUID's version or value.
//
//	commandContext := WithOpID(ctx, operationID)
//	attachedID, attached := OpIDFromContext(commandContext)
func OpIDFromContext(ctx context.Context) (uuid.UUID, bool) {
	opID, ok := ctx.Value(opIDKey{}).(uuid.UUID)
	return opID, ok
}

// WithOperatorPrincipal attaches the command operator to ctx.
//
// The returned context stores the principal under the operator principal key.
// It preserves the principal's fields without resolving another identity.
//
//	commandContext := WithOperatorPrincipal(ctx, principal)
//	attachedPrincipal, attached := OperatorPrincipalFromContext(commandContext)
func WithOperatorPrincipal(ctx context.Context, principal OperatorPrincipal) context.Context {
	return context.WithValue(ctx, operatorPrincipalKey{}, principal)
}

// OperatorPrincipalFromContext returns the command operator attached to ctx.
//
// The boolean reports whether the context contains an OperatorPrincipal under
// the operator principal key. A missing value returns the zero principal and
// false.
//
//	commandContext := WithOperatorPrincipal(ctx, principal)
//	attachedPrincipal, attached := OperatorPrincipalFromContext(commandContext)
func OperatorPrincipalFromContext(ctx context.Context) (OperatorPrincipal, bool) {
	principal, ok := ctx.Value(operatorPrincipalKey{}).(OperatorPrincipal)
	return principal, ok
}
