package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

// refusalWriteTimeout bounds the write after an access denial.
const refusalWriteTimeout = 30 * time.Second

// recordingUnavailableBody is the response body when the server cannot save a read event.
const recordingUnavailableBody = `{"error":"audit recording unavailable"}`

// requestRead contains the identity and timing of the read events for one request.
// The recorder saves one event for each organization in orgIDs. httpMethod and
// requestPath contain the checked HTTP method and request path. selection
// contains the normalized arguments that an authenticated read supplied.
type requestRead struct {
	operation   requestOperation
	operationID uuid.UUID
	startedAt   time.Time
	actor       audit.Actor
	orgIDs      []uuid.UUID
	requestID   string
	traceID     string
	httpMethod  string
	requestPath string
	selection   *audit.ReadSelection
}

// newRequestRead creates a requestRead with a new operation ID, the unauthenticated actor,
// and the system organization.
func newRequestRead(
	ctx context.Context,
	request *http.Request,
	operation requestOperation,
) requestRead {
	operationID := uuid.Must(uuid.NewV7())

	return requestRead{
		operation:   operation,
		operationID: operationID,
		startedAt:   clock.Now().UTC(),
		actor:       unauthenticatedActor(ctx),
		orgIDs:      []uuid.UUID{audit.SystemOrgID()},
		requestID:   telemetry.RequestID(ctx),
		traceID:     telemetry.TraceID(ctx),
		httpMethod:  checkedMethod(request.Method),
		requestPath: checkedPath(request.URL.Path),
		selection:   nil,
	}
}

// events builds one record per organization for an attempt or refusal.
func (read requestRead) events(decision audit.OutcomeDetails) []audit.Event {
	operation := read.operation
	name := string(operation.verb)

	events := make([]audit.Event, 0, len(read.orgIDs))
	for _, orgID := range read.orgIDs {
		eventID := uuid.Must(uuid.NewV7())
		target := audit.Target{
			Kind:         audit.TargetKindResource,
			NodeType:     "",
			Reference:    "",
			Name:         name,
			URI:          operation.route,
			RelationType: "",
			ID:           uuid.Nil,
			SourceID:     uuid.Nil,
			TargetID:     uuid.Nil,
		}
		class := audit.OperationRead
		if operation.mutation && read.actor.Type != audit.ActorUnauthenticated {
			class = audit.OperationDatabaseChange
			target = mutationTarget(operation.verb, operation.selection, target)
		}

		template := audit.Event{
			Verb:    string(operation.verb),
			EventID: eventID,
			Actor:   read.actor,
			Entity: audit.Entity{
				Type:       target.Kind,
				NodeType:   "",
				ID:         uuid.Nil,
				Identifier: "",
				Name:       name,
			},
			Context: audit.EventContext{
				OrgID:              orgID,
				WorkspaceID:        uuid.Nil,
				ScopeID:            uuid.Nil,
				ParentID:           uuid.Nil,
				RequestID:          read.requestID,
				TraceID:            read.traceID,
				Source:             operation.source,
				Tool:               operation.tool,
				RPC:                operation.method,
				Reason:             "",
				AuditFormatVersion: audit.AuditFormatComplete,
				OperationClass:     class,
				Phase:              audit.PhaseAttempt,
				OperationID:        read.operationID,
				StartedAt:          read.startedAt,
				Target:             &target,
				Selection:          read.selection,
				HTTPMethod:         read.httpMethod,
				RequestPath:        read.requestPath,
			},
			Delta:          nil,
			Outcome:        audit.OutcomeAttempted,
			Error:          nil,
			IdempotencyKey: "",
			OccurredAt:     read.startedAt,
			Extra:          nil,
		}
		if decision.Outcome != "" {
			template.Context.Phase = audit.PhaseRead
			template.Context.CompletedAt = clock.Now().UTC()
			template.Outcome = decision.Outcome
			template.Error = &audit.EventError{
				Code:    decision.FailureCode,
				Message: decision.FailureMessage,
			}
		}

		events = append(events, template)
	}

	return events
}

// serveRead records the access attempt before calling the handler.
func (recorder *RequestRecorder) serveRead(
	writer http.ResponseWriter,
	request *http.Request,
	read requestRead,
	next http.Handler,
) {
	if !recorder.record(writer, request, read, audit.NoOutcome()) {
		return
	}

	ctx := audit.WithReadCollector(request.Context())
	capture := newResponseCapture()
	next.ServeHTTP(capture, request.WithContext(ctx))
	decision := audit.ReadDecision(ctx)
	if decision.Outcome == "" {
		decision = statusOutcome(capture.status)
	}

	if decision.Outcome == audit.OutcomeRefused &&
		!recorder.record(writer, request, read, decision) {
		return
	}

	capture.release(writer, capture.body.Bytes())
}

// respond records an access decision before returning the response.
func (recorder *RequestRecorder) respond(
	writer http.ResponseWriter,
	request *http.Request,
	read requestRead,
	capture *responseCapture,
	decision audit.OutcomeDetails,
) {
	if recorder.record(writer, request, read, decision) {
		capture.release(writer, capture.body.Bytes())
	}
}

func (recorder *RequestRecorder) record(
	writer http.ResponseWriter,
	request *http.Request,
	read requestRead,
	decision audit.OutcomeDetails,
) bool {
	events := read.events(decision)
	for _, event := range events {
		if err := audit.ValidateEvent(event); err != nil {
			recordingUnavailable(request.Context(), writer, read, err)
			return false
		}
	}

	ctx := request.Context()
	if decision.Outcome == audit.OutcomeRefused {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), refusalWriteTimeout)
		defer cancel()
	}

	if err := recorder.store.WriteOutboxBatch(ctx, events); err != nil {
		recordingUnavailable(request.Context(), writer, read, err)
		return false
	}

	return true
}

// recordingUnavailable logs the recording failure and answers with 503.
func recordingUnavailable(
	ctx context.Context,
	writer http.ResponseWriter,
	read requestRead,
	err error,
) {
	if err == nil {
		err = fmt.Errorf("read %s did not produce a response", read.operationID)
	}

	telemetry.L(ctx).ErrorContext(
		ctx,
		"mcp.audit_request.not_recorded",
		slog.String("operation_id", read.operationID.String()),
		slog.String("verb", string(read.operation.verb)),
		slog.String("err", err.Error()),
	)
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = writer.Write([]byte(recordingUnavailableBody))
}

// unauthenticatedActor returns the actor of a request before authentication.
func unauthenticatedActor(ctx context.Context) audit.Actor {
	return audit.Actor{
		Type:          audit.ActorUnauthenticated,
		ID:            uuid.Nil,
		Email:         "",
		Name:          "",
		SessionID:     "",
		IP:            "",
		UserAgent:     "",
		RequestID:     telemetry.RequestID(ctx),
		APITokenLabel: "",
	}
}

// userActor returns the verified user actor of an authenticated request.
func userActor(ctx context.Context, userID uuid.UUID) audit.Actor {
	return audit.Actor{
		Type:          audit.ActorUser,
		ID:            userID,
		Email:         "",
		Name:          "",
		SessionID:     "",
		IP:            "",
		UserAgent:     "",
		RequestID:     telemetry.RequestID(ctx),
		APITokenLabel: "",
	}
}
