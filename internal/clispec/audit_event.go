package clispec

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/response"
)

// newOperatorEvent builds the event a command records. Extra stores the
// operation ID that a reader uses to pair intent and outcome. The ledger
// stores Extra and includes it in the row hash. Changing either row
// invalidates its hash chain.
func newOperatorEvent(
	ctx context.Context,
	spec audit.Spec,
	principal audit.OperatorPrincipal,
	opID uuid.UUID,
	deployCommit string,
) (audit.Event, error) {
	extra, err := json.Marshal(operatorEventExtra{
		OpID:           opID,
		StartedAt:      nil,
		OperatorSource: principal.Source,
		DeployCommit:   strings.TrimSpace(deployCommit),
		SessionID:      principal.SessionID,
		OnBehalfOf:     principal.OnBehalfOf,
	})
	if err != nil {
		return audit.Event{}, loggedAuditError(ctx, "encode operator event extra", err)
	}

	correlation := response.FromContext(ctx)

	return audit.Event{
		Verb:    spec.Verb,
		EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type:          principal.ActorType(),
			ID:            principal.ID,
			Email:         principal.Email,
			Name:          principal.Name,
			SessionID:     principal.SessionID,
			IP:            "",
			UserAgent:     "",
			RequestID:     correlation.RequestID,
			APITokenLabel: "",
		},
		Entity: audit.Entity{
			Type:       "system",
			NodeType:   "",
			ID:         audit.SystemOrgID(),
			Identifier: "",
			Name:       "",
		},
		Context: audit.EventContext{
			OrgID:       audit.SystemOrgID(),
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
			RequestID:   correlation.RequestID,
			TraceID:     correlation.TraceID,
			Source:      audit.SourceSystem,
			Tool:        "",
			RPC:         "",
			Reason:      "",
		},
		Delta:          nil,
		Outcome:        audit.OutcomeOK,
		Error:          nil,
		IdempotencyKey: "",
		OccurredAt:     clock.Now().UTC(),
		Extra:          extra,
	}, nil
}

// operatorEventExtra stores the operation ID and operator details for events.
type operatorEventExtra struct {
	// OpID associates an intent row with its outcome row.
	OpID uuid.UUID `json:"op_id"`
	// StartedAt records when the deferred infrastructure command began running.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// OperatorSource identifies how the command established the identity.
	// A reader uses it to distinguish a Git configuration identity from an
	// identity the operator supplied through a flag.
	OperatorSource string `json:"operator_source"`
	// DeployCommit records the opaque commit or branch that the deploy
	// playbook supplies.
	DeployCommit string `json:"deploy_commit,omitempty"`
	// SessionID identifies the agent session of a service principal. The
	// ledger table does not have a session column. The row hash covers this
	// field in Extra.
	SessionID string `json:"session_id,omitempty"`
	// OnBehalfOf identifies the accountable human operator when a service
	// principal runs the command for that operator.
	OnBehalfOf *audit.ActProvenance `json:"on_behalf_of,omitempty"`
	Input      json.RawMessage      `json:"input,omitempty" exhaustruct:"optional"`
}
