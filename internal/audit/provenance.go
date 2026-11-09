package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// ActProvenance identifies the operator behind a write that records a product
// user as its actor. The user's ledger row includes the provenance in its row
// hash and records the grant and reason for the change (TACK-424).
//
// OperatorID and OperatorEmail identify the accountable person. When an agent
// acts for that person, the Agent fields identify its service and session.
type ActProvenance struct {
	OperatorID     uuid.UUID `json:"operator_id"`
	OperatorEmail  string    `json:"operator_email,omitempty"`
	GrantID        uuid.UUID `json:"grant_id"`
	Reason         string    `json:"reason"`
	AgentName      string    `exhaustruct:"optional" json:"agent_name,omitempty"`
	AgentID        uuid.UUID `exhaustruct:"optional" json:"agent_id,omitzero"`
	AgentSessionID string    `exhaustruct:"optional" json:"agent_session_id,omitempty"`
}

type provenanceKey struct{}

// WithActProvenance attaches operator provenance to ctx for an act-as write.
//
// The audit slot operation uses stagedExtra to include this provenance in its
// Extra payload unless the nearest audit slot contains an encoded operation.
//
//	writeContext := WithActProvenance(ctx, provenance)
//	attached, present := ActProvenanceFromContext(writeContext)
func WithActProvenance(ctx context.Context, provenance ActProvenance) context.Context {
	return context.WithValue(ctx, provenanceKey{}, provenance)
}

// ActProvenanceFromContext returns the provenance attached to ctx.
//
// The boolean reports whether the context contains an ActProvenance value.
// A context without that value returns the zero value and false.
//
//	writeContext := WithActProvenance(ctx, provenance)
//	attached, present := ActProvenanceFromContext(writeContext)
func ActProvenanceFromContext(ctx context.Context) (ActProvenance, bool) {
	provenance, ok := ctx.Value(provenanceKey{}).(ActProvenance)
	return provenance, ok
}

// actAsExtra is the Extra payload of a row written on a user's behalf.
type actAsExtra struct {
	ActAs ActProvenance `json:"act_as"`
}

// stagedExtra returns the act-as provenance as an Extra payload when ctx
// contains provenance. It returns nil otherwise.
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

// stagedSource returns SourceOperator when an operator acts as the user.
// It returns SourceMCP otherwise.
func stagedSource(ctx context.Context) Source {
	if _, ok := ActProvenanceFromContext(ctx); ok {
		return SourceOperator
	}
	return SourceMCP
}
