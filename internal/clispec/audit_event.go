package clispec

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// newOperatorEvent builds the event a command records. Extra stores the op
// id. The ledger stores Extra and the row hash covers it. A reader pairs the
// intent row with its outcome row by op id, and altering either row breaks
// the chain.
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
			RequestID:     "",
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
			RequestID:   "",
			TraceID:     "",
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

// operatorEventExtra is the correlation payload of every operator event.
type operatorEventExtra struct {
	// OpID is shared by an intent row and its outcome row.
	OpID uuid.UUID `json:"op_id"`
	// StartedAt records when a deferred infrastructure command entered the gate.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// OperatorSource identifies the mechanism that established the identity.
	// A reader uses it to tell a git-config identity from an asserted flag.
	OperatorSource string `json:"operator_source"`
	// DeployCommit identifies the opaque commit or branch supplied by the deploy playbook.
	DeployCommit string `json:"deploy_commit,omitempty"`
	// SessionID identifies the agent session of a service principal. The
	// ledger table has no session column; the row hash covers this field.
	SessionID string `json:"session_id,omitempty"`
	// OnBehalfOf identifies the accountable human operator when a service
	// principal runs the command for that operator.
	OnBehalfOf *audit.ActProvenance `json:"on_behalf_of,omitempty"`
}
