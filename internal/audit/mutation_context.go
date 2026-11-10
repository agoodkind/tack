package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/auditintent"
	"goodkind.io/tack/internal/telemetry"
)

// ErrMissingMutationContext reports a missing or invalid audit operation for
// a database write.
var ErrMissingMutationContext = errors.New("the write requires an audit mutation context")

// uuidVersionTimeOrdered is the UUID version that encodes its creation time.
const uuidVersionTimeOrdered = 7

// WithMutationContext encodes operation and attaches it to ctx with
// [auditintent.WithOperation].
//
// [RequireMutationContext] reads the attached operation unless a caller
// attaches a nearer audit slot. An encoding error returns the original ctx
// with an error.
//
//  1. Encode and attach the operation with WithMutationContext.
//  2. Pass the returned context to the database write.
func WithMutationContext(ctx context.Context, operation Event) (context.Context, error) {
	encoded, err := MarshalEvent(operation)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.mutation_context.encode_failed",
			slog.String("err", err.Error()),
		)

		return ctx, fmt.Errorf(
			"encode the audit operation %s: %w",
			operation.Context.OperationID,
			err,
		)
	}

	return auditintent.WithOperation(ctx, encoded), nil
}

// NewMutationOperation creates an operation with a new UUIDv7 operation ID.
//
// The operation's start time equals the time in that UUID. Its actor, source,
// and tool come from the arguments.
//
//   - UUID creation errors return an error instead of an operation.
//   - The returned event stores its start time in both StartedAt and
//     OccurredAt.
func NewMutationOperation(actor Actor, source Source, tool string) (Event, error) {
	operationID, err := uuid.NewV7()
	if err != nil {
		slog.Error("audit.mutation_context.id_failed", slog.String("err", err.Error()))
		return Event{}, fmt.Errorf("create the audit operation ID: %w", err)
	}

	return operationEvent(
		actor,
		operationID,
		EventContext{
			OrgID:       uuid.Nil,
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
			RequestID:   "",
			TraceID:     "",
			Source:      source,
			Tool:        tool,
			RPC:         "",
			Reason:      "",
		},
		nil,
	), nil
}

// RequireMutationContext reads the operation from the nearest audit slot on
// ctx.
//
// The slot can contain an operation from [WithMutationContext] or
// [auditintent.WithOperation], or an MCP tool call from [auditintent.WithSlot].
// Without a slot, the function reads the operator principal and UUIDv7
// operation ID that the clispec command gate attaches.
//
// An error wraps [ErrMissingMutationContext] when any required value is missing
// or invalid:
//
//   - The context must provide an operation.
//   - The actor must have an ID and a known kind.
//   - The operation must have an ID and a start time.
func RequireMutationContext(ctx context.Context) (Event, error) {
	operation, found, err := findMutationContext(ctx)
	if err != nil {
		return Event{}, err
	}

	if !found {
		return Event{}, missingMutationContext(
			ctx,
			"ctx does not contain an audit operation, an MCP tool slot, or an operator principal with an operation ID",
		)
	}

	switch {
	case operation.Actor.ID == uuid.Nil || actorKindCode(operation.Actor.Type) == 0:
		return Event{}, missingMutationContext(
			ctx,
			"the operation does not have both an actor ID and a known actor kind",
		)
	case operation.Context.OperationID == uuid.Nil || operation.Context.StartedAt.IsZero():
		return Event{}, missingMutationContext(
			ctx,
			"the operation does not have both an operation ID and a start time",
		)
	}

	return operation, nil
}

// WithMutationTool reads the operation with [RequireMutationContext] and sets
// the operation's tool to tool.
//
// The function attaches the updated operation to the returned context with
// [WithMutationContext]. A read or encoding error returns the original context
// and the error.
//
//  1. Pass the existing operation context to WithMutationTool.
//  2. Use the returned context for work under the updated tool.
func WithMutationTool(ctx context.Context, tool string) (context.Context, error) {
	operation, err := RequireMutationContext(ctx)
	if err != nil {
		return ctx, err
	}

	operation.Context.Tool = tool

	return WithMutationContext(ctx, operation)
}

// findMutationContext reads the operation from the nearest slot on ctx, or
// from the operator principal and operation ID when ctx does not have a slot.
func findMutationContext(ctx context.Context) (Event, bool, error) {
	if encoded := auditintent.Operation(ctx); encoded != nil {
		var operation Event
		if err := json.Unmarshal(encoded, &operation); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.mutation_context.decode_failed",
				slog.String("err", err.Error()),
			)

			return operation, false, fmt.Errorf("decode the attached audit operation: %w", err)
		}

		return operation, true, nil
	}

	if auditintent.Attached(ctx) {
		operation, err := slotOperation(ctx)
		return operation, err == nil, err
	}

	principal, hasPrincipal := OperatorPrincipalFromContext(ctx)
	opID, hasOpID := ctx.Value(opIDKey{}).(uuid.UUID)
	if !hasPrincipal || !hasOpID {
		var none Event
		return none, false, nil
	}

	if opID.Version() != uuidVersionTimeOrdered {
		return Event{}, false, missingMutationContext(
			ctx,
			"the operator command operation ID is not a UUIDv7",
		)
	}

	actor := Actor{
		Type:          principal.ActorType(),
		ID:            principal.ID,
		Email:         principal.Email,
		Name:          principal.Name,
		SessionID:     principal.SessionID,
		IP:            "",
		UserAgent:     "",
		RequestID:     telemetry.RequestID(ctx),
		APITokenLabel: "",
	}

	return operationEvent(actor, opID, requestContext(ctx, SourceSystem, ""), nil), true, nil
}

// slotOperation builds an operation from the nearest MCP tool or act-as slot
// on ctx. It sets ParentOperationID to the operator command's operation ID
// when that ID exists and differs from the slot's operation ID.
func slotOperation(ctx context.Context) (Event, error) {
	operationID, err := auditintent.OperationID(ctx)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.mutation_context.slot_failed",
			slog.String("err", err.Error()),
		)

		return Event{}, fmt.Errorf("create the audit slot operation ID: %w", err)
	}

	extra, err := stagedExtra(ctx)
	if err != nil {
		return Event{}, err
	}

	actor := Actor{
		Type:          ActorUser,
		ID:            auditintent.Actor(ctx),
		Email:         "",
		Name:          "",
		SessionID:     "",
		IP:            "",
		UserAgent:     "",
		RequestID:     telemetry.RequestID(ctx),
		APITokenLabel: "",
	}
	eventContext := requestContext(ctx, stagedSource(ctx), auditintent.Tool(ctx))
	scope := ScopeFromContext(ctx)
	eventContext.OrgID, eventContext.WorkspaceID = scope.OrgID, scope.WorkspaceID
	eventContext.ScopeID, eventContext.ParentID = scope.ScopeID, scope.ParentID
	operation := operationEvent(actor, operationID, eventContext, extra)
	if parentID, ok := ctx.Value(opIDKey{}).(uuid.UUID); ok && parentID != operationID {
		operation.Context.ParentOperationID = parentID
	}

	return operation, nil
}

func requestContext(ctx context.Context, source Source, tool string) EventContext {
	return EventContext{
		OrgID:       uuid.Nil,
		WorkspaceID: uuid.Nil,
		ScopeID:     uuid.Nil,
		ParentID:    uuid.Nil,
		RequestID:   telemetry.RequestID(ctx),
		TraceID:     telemetry.TraceID(ctx),
		Source:      source,
		Tool:        tool,
		RPC:         "",
		Reason:      "",
	}
}

// operationEvent returns the operation event for actor and operationID. The
// start time is the time that the UUIDv7 operationID encodes.
func operationEvent(
	actor Actor,
	operationID uuid.UUID,
	eventContext EventContext,
	extra []byte,
) Event {
	seconds, nanoseconds := operationID.Time().UnixTime()
	eventContext.OperationID = operationID
	eventContext.StartedAt = time.Unix(seconds, nanoseconds).UTC()

	return Event{
		Verb:           "",
		EventID:        uuid.Nil,
		Actor:          actor,
		Entity:         Entity{Type: "", NodeType: "", ID: uuid.Nil, Identifier: "", Name: ""},
		Context:        eventContext,
		Delta:          nil,
		Outcome:        "",
		Error:          nil,
		IdempotencyKey: "",
		OccurredAt:     eventContext.StartedAt,
		Extra:          extra,
	}
}

func missingMutationContext(ctx context.Context, reason string) error {
	err := fmt.Errorf("%w: %s", ErrMissingMutationContext, reason)
	slog.ErrorContext(ctx, "audit.mutation_context.missing", slog.String("err", err.Error()))

	return err
}
