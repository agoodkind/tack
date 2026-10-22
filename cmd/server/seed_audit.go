package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
)

type seedOutboxRecorder struct {
	outbox audit.OutboxWriter
}

func (r seedOutboxRecorder) Record(ctx context.Context, event audit.Event) error {
	if err := r.outbox.WriteOutbox(ctx, event); err != nil {
		slog.ErrorContext(ctx, "seed.audit_outbox_failed", slog.String("err", err.Error()))
		return fmt.Errorf("write seed audit event: %w", err)
	}
	return nil
}

// seedOperation builds a seed change operation from the operator principal in
// ctx. It returns an error when ctx does not contain a principal.
func seedOperation(ctx context.Context, verb audit.Verb, orgID uuid.UUID) (audit.Event, error) {
	principal, found := audit.OperatorPrincipalFromContext(ctx)
	if !found {
		err := errors.New("seed change has no operator principal")
		slog.ErrorContext(ctx, "seed.audit_principal_missing", slog.String("err", err.Error()))
		return audit.Event{}, err
	}
	return postgres.NewChangeOperation(verb, postgres.OperatorActor(principal), orgID, audit.SourceSeed), nil
}

func recordSeedNode(
	ctx context.Context,
	recorder audit.Recorder,
	nodeID uuid.UUID,
	orgID uuid.UUID,
	parentID uuid.UUID,
	nodeType string,
	slug string,
	name string,
) error {
	return recordSeedEvent(ctx, recorder, audit.VerbNodeCreate, audit.Entity{
		Type: "node", NodeType: nodeType, ID: nodeID, Identifier: slug, Name: name,
	}, orgID, uuid.Nil, parentID)
}

func recordSeedNodeType(
	ctx context.Context,
	recorder audit.Recorder,
	verb audit.Verb,
	nodeType *node.NodeType,
) error {
	return recordSeedEvent(ctx, recorder, verb, audit.Entity{
		Type: "node_type", NodeType: nodeType.TypeKey, ID: nodeType.ID, Identifier: nodeType.Slug, Name: nodeType.Name,
	}, nodeType.OrgID, uuid.Nil, uuid.Nil)
}

func recordSeedPropertyDef(
	ctx context.Context,
	recorder audit.Recorder,
	verb audit.Verb,
	propertyDef *node.PropertyDef,
) error {
	return recordSeedEvent(ctx, recorder, verb, audit.Entity{
		Type: "property_def", NodeType: "", ID: propertyDef.ID, Identifier: propertyDef.Name, Name: propertyDef.Name,
	}, propertyDef.OrgID, uuid.Nil, uuid.Nil)
}

// recordSeedNodeTypeIntent records that a node type is about to be written.
func recordSeedNodeTypeIntent(
	ctx context.Context,
	recorder audit.Recorder,
	verb audit.Verb,
	nodeType *node.NodeType,
) error {
	return recordSeedOutcome(ctx, recorder, verb, audit.Entity{
		Type: "node_type", NodeType: nodeType.TypeKey, ID: nodeType.ID, Identifier: nodeType.Slug, Name: nodeType.Name,
	}, nodeType.OrgID, uuid.Nil, uuid.Nil, audit.OutcomePending)
}

// recordSeedPropertyDefIntent records that a property definition is about to
// be written.
func recordSeedPropertyDefIntent(
	ctx context.Context,
	recorder audit.Recorder,
	verb audit.Verb,
	propertyDef *node.PropertyDef,
) error {
	return recordSeedOutcome(ctx, recorder, verb, audit.Entity{
		Type: "property_def", NodeType: "", ID: propertyDef.ID, Identifier: propertyDef.Name, Name: propertyDef.Name,
	}, propertyDef.OrgID, uuid.Nil, uuid.Nil, audit.OutcomePending)
}

func recordSeedEvent(
	ctx context.Context,
	recorder audit.Recorder,
	verb audit.Verb,
	entity audit.Entity,
	orgID uuid.UUID,
	workspaceID uuid.UUID,
	parentID uuid.UUID,
) error {
	return recordSeedOutcome(ctx, recorder, verb, entity, orgID, workspaceID, parentID, audit.OutcomeOK)
}

func recordSeedOutcome(
	ctx context.Context,
	recorder audit.Recorder,
	verb audit.Verb,
	entity audit.Entity,
	orgID uuid.UUID,
	workspaceID uuid.UUID,
	parentID uuid.UUID,
	outcome audit.Outcome,
) error {
	principal, found := audit.OperatorPrincipalFromContext(ctx)
	if !found {
		err := errors.New("seed audit event has no operator principal")
		slog.ErrorContext(ctx, "seed.audit_principal_missing", slog.String("err", err.Error()))
		return err
	}
	event := audit.Event{
		Verb: string(verb), EventID: uuid.Nil,
		Actor: audit.Actor{
			Type: principal.ActorType(), ID: principal.ID, Email: principal.Email, Name: principal.Name,
			SessionID: "", IP: "", UserAgent: "", RequestID: "", APITokenLabel: "",
		},
		Entity: entity,
		Context: audit.EventContext{
			OrgID: orgID, WorkspaceID: workspaceID, ScopeID: uuid.Nil, ParentID: parentID,
			RequestID: "", TraceID: "", Source: audit.SourceSeed, Tool: "", RPC: "", Reason: "",
		},
		Delta: nil, Outcome: outcome, Error: nil, IdempotencyKey: "", OccurredAt: time.Time{}, Extra: nil,
	}
	if err := recorder.Record(ctx, event); err != nil {
		slog.ErrorContext(ctx, "seed.audit_record_failed",
			slog.String("verb", event.Verb),
			slog.String("entity_id", event.Entity.ID.String()),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("record seed %s: %w", event.Verb, err)
	}
	return nil
}

// seedMembership grants the seeded user the owner role through
// OrgMemberRepo.AddMember, which records any membership change.
func seedMembership(
	ctx context.Context,
	members *postgres.OrgMemberRepo,
	orgID, userID uuid.UUID,
) error {
	operation, err := seedOperation(ctx, audit.VerbOrgMemberAdd, orgID)
	if err != nil {
		return err
	}
	member := &org.Member{
		ID: uuid.Nil, OrgID: orgID, UserID: userID,
		Role: seedOwnerRole, CreatedAt: time.Time{},
	}
	if err := members.AddMember(ctx, operation, member); err != nil {
		slog.ErrorContext(ctx, "seed.add_member_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: add member: %w", err)
	}
	return nil
}

// seedOwnerRole is the org role the seeded user receives. It matches the
// numeric role the seed has always granted.
const seedOwnerRole = 20
