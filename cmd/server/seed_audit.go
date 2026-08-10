package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/node"
	"goodkind.io/tack/internal/domain/org"
	"goodkind.io/tack/internal/domain/token"
	"goodkind.io/tack/internal/domain/user"
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

func recordSeedUser(ctx context.Context, recorder audit.Recorder, seededUser *user.User) error {
	return recordSeedEvent(ctx, recorder, audit.VerbUserCreate, audit.Entity{
		Type: "user", NodeType: "", ID: seededUser.ID, Identifier: seededUser.Email, Name: seededUser.DisplayName,
	}, audit.SystemOrgID(), uuid.Nil, uuid.Nil)
}

func recordSeedToken(ctx context.Context, recorder audit.Recorder, seededToken *token.Token, orgID uuid.UUID) error {
	return recordSeedEvent(ctx, recorder, audit.VerbAuthTokenCreate, audit.Entity{
		Type: "api_token", NodeType: "", ID: seededToken.ID, Identifier: "", Name: seededToken.Label,
	}, orgID, uuid.Nil, uuid.Nil)
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

// recordSeedMembershipIntent records that the seeded user is about to be
// granted their org role.
func recordSeedMembershipIntent(
	ctx context.Context,
	recorder audit.Recorder,
	orgID, userID uuid.UUID,
) error {
	return recordSeedOutcome(ctx, recorder, audit.VerbOrgMemberAdd, audit.Entity{
		Type: "org_member", NodeType: "", ID: userID, Identifier: "", Name: "",
	}, orgID, uuid.Nil, orgID, audit.OutcomePending)
}

// recordSeedMembership records that the seeded user holds their org role.
func recordSeedMembership(
	ctx context.Context,
	recorder audit.Recorder,
	orgID, userID uuid.UUID,
) error {
	return recordSeedOutcome(ctx, recorder, audit.VerbOrgMemberAdd, audit.Entity{
		Type: "org_member", NodeType: "", ID: userID, Identifier: "", Name: "",
	}, orgID, uuid.Nil, orgID, audit.OutcomeOK)
}

// seedMembership grants the seeded user their org role, recording the intent
// before the change and the result after.
//
// Membership is a privilege change, so it belongs in the ledger like every
// other seed write. Its failure used to be logged and swallowed, which left
// seed reporting success while the user held no membership at all.
func seedMembership(
	ctx context.Context,
	members org.MemberRepository,
	recorder audit.Recorder,
	orgID, userID uuid.UUID,
) error {
	if err := recordSeedMembershipIntent(ctx, recorder, orgID, userID); err != nil {
		return err
	}
	member := &org.Member{
		ID: uuid.Nil, OrgID: orgID, UserID: userID,
		Role: seedOwnerRole, CreatedAt: time.Time{},
	}
	err := members.AddMember(ctx, member)
	if err != nil && !errors.Is(err, domain.ErrAlreadyExists) {
		slog.ErrorContext(ctx, "seed.add_member_failed", slog.String("err", err.Error()))
		return fmt.Errorf("seed: add member: %w", err)
	}
	return recordSeedMembership(ctx, recorder, orgID, userID)
}

// seedOwnerRole is the org role the seeded user receives. It matches the
// numeric role the seed has always granted.
const seedOwnerRole = 20
