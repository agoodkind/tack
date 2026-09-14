package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ActProvenance names the operator behind a write recorded as a product user.
// It rides on the user's own ledger row, under the row hash, so the row keeps
// the user as its actor and still says which operator made the change, under
// which grant, and why (TACK-424).
type ActProvenance struct {
	OperatorID    uuid.UUID `json:"operator_id"`
	OperatorEmail string    `json:"operator_email,omitempty"`
	GrantID       uuid.UUID `json:"grant_id"`
	Reason        string    `json:"reason"`
}

type provenanceKey struct{}

// WithActProvenance attaches the operator behind an act-as write to ctx, so
// every state change staged under ctx carries it.
func WithActProvenance(ctx context.Context, provenance ActProvenance) context.Context {
	return context.WithValue(ctx, provenanceKey{}, provenance)
}

// ActProvenanceFromContext returns the provenance attached to ctx, if any.
func ActProvenanceFromContext(ctx context.Context) (ActProvenance, bool) {
	provenance, ok := ctx.Value(provenanceKey{}).(ActProvenance)
	return provenance, ok
}

// actAsExtra is the Extra payload of a row written on a user's behalf.
type actAsExtra struct {
	ActAs ActProvenance `json:"act_as"`
}

// stagedExtra returns the Extra payload a staged event carries: the act-as
// provenance when ctx has one, nothing otherwise.
func stagedExtra(ctx context.Context) (json.RawMessage, error) {
	provenance, ok := ActProvenanceFromContext(ctx)
	if !ok {
		return nil, nil
	}
	encoded, err := json.Marshal(actAsExtra{ActAs: provenance})
	if err != nil {
		slog.ErrorContext(ctx, "audit.provenance_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode act-as provenance: %w", err)
	}
	return encoded, nil
}

// stagedSource is the source a staged event records: the operator surface
// when an operator acts as the user, the MCP surface otherwise.
func stagedSource(ctx context.Context) Source {
	if _, ok := ActProvenanceFromContext(ctx); ok {
		return SourceOperator
	}
	return SourceMCP
}
