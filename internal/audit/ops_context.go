package audit

import (
	"context"

	"github.com/google/uuid"
)

type opIDKey struct{}

type operatorPrincipalKey struct{}

// WithOpID attaches the operation identifier that correlates the intent row a
// command writes before it runs with the outcome row it writes after.
func WithOpID(ctx context.Context, opID uuid.UUID) context.Context {
	return context.WithValue(ctx, opIDKey{}, opID)
}

// WithOperatorPrincipal attaches the command operator to ctx.
func WithOperatorPrincipal(ctx context.Context, principal OperatorPrincipal) context.Context {
	return context.WithValue(ctx, operatorPrincipalKey{}, principal)
}

// OperatorPrincipalFromContext returns the command operator attached to ctx.
func OperatorPrincipalFromContext(ctx context.Context) (OperatorPrincipal, bool) {
	principal, ok := ctx.Value(operatorPrincipalKey{}).(OperatorPrincipal)
	return principal, ok
}
