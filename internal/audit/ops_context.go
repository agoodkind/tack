package audit

import (
	"context"

	"github.com/google/uuid"
)

type opIDKey struct{}

// WithOpID attaches the operation identifier that correlates the intent row a
// command writes before it runs with the outcome row it writes after.
func WithOpID(ctx context.Context, opID uuid.UUID) context.Context {
	return context.WithValue(ctx, opIDKey{}, opID)
}
