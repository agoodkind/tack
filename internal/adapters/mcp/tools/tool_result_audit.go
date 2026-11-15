package tools

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
	"goodkind.io/tack/internal/telemetry"
)

const (
	// toolResultRoute is the resource URI of a tool result without a resolved target.
	toolResultRoute = "/mcp"
)

// recordToolResult records refusals from state-changing tools.
func recordToolResult(
	ctx context.Context,
	recorder audit.Recorder,
	toolName string,
) {
	outcome, reason, noted := toolNoteResult(ctx)
	if outcome.Outcome != audit.OutcomeRefused {
		return
	}

	event, err := toolResultEvent(
		ctx,
		toolName,
		outcome,
		reason,
		toolResultTarget(ctx, toolName, noted),
	)
	if err == nil {
		// The audit-consumer stores an event that fails validation in the
		// dead-letter table for repair.
		if invalid := audit.ValidateEvent(event); invalid != nil {
			telemetry.L(ctx).ErrorContext(
				ctx,
				"mcp.tool.result_invalid",
				slog.String("tool", toolName),
				slog.String("err", invalid.Error()),
			)
		}

		err = recorder.Record(ctx, event)
	}

	if err != nil {
		telemetry.L(ctx).ErrorContext(
			ctx,
			"mcp.tool.result_not_recorded",
			slog.String("tool", toolName),
			slog.String("err", err.Error()),
		)
	}
}

// toolChangesState reports whether toolName resolves to a state-change verb.
func toolChangesState(toolName string) bool {
	verb, covered := audit.ToolVerb(toolName)
	return covered && verb != "" && !audit.IsRead(verb)
}

// toolResultTarget returns the stored target, the node that the handler
// resolved, or the MCP route with the tool name, in that order.
func toolResultTarget(ctx context.Context, toolName string, noted *audit.Target) audit.Target {
	if noted != nil {
		return *noted
	}

	entity := audit.EntityFromContext(ctx)
	if entity.ID != uuid.Nil && entity.NodeType != "" {
		return nodeTarget(entity.NodeType, entity.ID)
	}

	return audit.Target{
		Kind:         audit.TargetKindResource,
		NodeType:     "",
		Reference:    "",
		Name:         toolName,
		URI:          toolResultRoute,
		RelationType: "",
		ID:           uuid.Nil,
		SourceID:     uuid.Nil,
		TargetID:     uuid.Nil,
	}
}

// toolResultEvent builds the result event from the audit operation of the
// tool call. It uses the system organization when the handler did not
// resolve an organization.
func toolResultEvent(
	ctx context.Context,
	toolName string,
	outcome audit.OutcomeDetails,
	reason string,
	target audit.Target,
) (audit.Event, error) {
	operation, err := audit.RequireMutationContext(ctx)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"mcp.tool.result_operation_missing",
			slog.String("tool", toolName),
			slog.String("err", err.Error()),
		)

		return audit.Event{}, fmt.Errorf("read the audit operation of tool %s: %w", toolName, err)
	}

	verb, _ := audit.ToolVerb(toolName)
	completedAt := clock.Now().UTC()
	if completedAt.Before(operation.Context.StartedAt) {
		completedAt = operation.Context.StartedAt
	}

	event := operation
	event.Verb, event.EventID, event.OccurredAt = string(verb), uuid.Must(uuid.NewV7()), completedAt
	event.Entity = audit.Entity{
		Type:       target.Kind,
		NodeType:   target.NodeType,
		ID:         target.ID,
		Identifier: target.Reference,
		Name:       target.Name,
	}
	if event.Context.OrgID == uuid.Nil {
		event.Context.OrgID = audit.SystemOrgID()
	}

	event.Context.Tool, event.Context.Reason = toolName, reason
	event.Context.AuditFormatVersion = audit.AuditFormatComplete
	event.Context.OperationClass, event.Context.Phase = audit.OperationDatabaseChange, audit.PhaseResult
	event.Context.CompletedAt, event.Context.Target = completedAt, &target
	event.Context.CommittedSteps = outcome.CommittedSteps
	event.Outcome, event.Error = outcome.Outcome, nil
	if outcome.Outcome != audit.OutcomeCompleted {
		event.Error = &audit.EventError{Code: outcome.FailureCode, Message: outcome.FailureMessage}
	}

	return event, nil
}
