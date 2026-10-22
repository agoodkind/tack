package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

func operationTarget(ctx context.Context, spec audit.Spec, input json.RawMessage) (audit.Target, json.RawMessage, error) {
	if err := audit.ValidateSpec(spec); err != nil {
		return audit.Target{}, nil, loggedAuditError(ctx, "validate audit spec", err)
	}
	targets, err := spec.Targets(ctx, input)
	if err != nil {
		return audit.Target{}, nil, loggedAuditError(ctx, "select audit target", err)
	}
	if len(targets) != 1 {
		return audit.Target{}, nil, loggedAuditError(ctx, "select audit target",
			fmt.Errorf("audit spec %q selected %d targets, and an operator command requires exactly one", spec.Verb, len(targets)))
	}
	safeInput, err := spec.SafeInput(input)
	if err != nil {
		return audit.Target{}, nil, loggedAuditError(ctx, "select safe audit input", err)
	}
	return targets[0], safeInput, nil
}

func newCompleteOperatorEvent(
	ctx context.Context,
	spec audit.Spec,
	principal audit.OperatorPrincipal,
	operationID uuid.UUID,
	deployCommit string,
	target audit.Target,
	phase audit.Phase,
	safeInput json.RawMessage,
) (audit.Event, error) {
	if principal.ID == uuid.Nil {
		return audit.Event{}, loggedAuditError(ctx, "build audit event", errors.New("operator principal ID is nil"))
	}
	extra, err := json.Marshal(operatorEventExtra{
		OpID: operationID, StartedAt: nil, OperatorSource: principal.Source,
		DeployCommit: strings.TrimSpace(deployCommit), SessionID: principal.SessionID,
		OnBehalfOf: principal.OnBehalfOf, Input: safeInput,
	})
	if err != nil {
		return audit.Event{}, loggedAuditError(ctx, "encode operator event extra", err)
	}
	now := clock.Now().UTC()
	eventTarget := target
	return audit.Event{
		Verb: spec.Verb, EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: principal.SessionID, IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: audit.Entity{
			Type: target.Kind, NodeType: target.NodeType, ID: target.ID, Identifier: target.Reference, Name: target.Name,
		},
		Context: audit.EventContext{
			OrgID: audit.SystemOrgID(), WorkspaceID: uuid.Nil, ScopeID: uuid.Nil, ParentID: uuid.Nil,
			RequestID: "", TraceID: "", Source: audit.SourceSystem, Tool: "", RPC: "", Reason: "",
			AuditFormatVersion: audit.AuditFormatComplete, OperationClass: spec.Class, Phase: phase,
			OperationID: operationID, ParentOperationID: uuid.Nil, StartedAt: now, CompletedAt: time.Time{},
			Target: &eventTarget, Read: nil, CommittedSteps: nil,
		},
		Delta: nil, Outcome: "", Error: nil, IdempotencyKey: "", OccurredAt: now, Extra: extra,
	}, nil
}
