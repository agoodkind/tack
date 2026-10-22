package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
)

const (
	// commandConfigCode is the error code of an external command that refused
	// to run because its configuration is incomplete.
	commandConfigCode = "configuration_incomplete"
	commandFailedCode = "step_failed"
)

func externalTarget(reference string) audit.Target {
	return audit.Target{
		Kind: audit.TargetKindExternal, NodeType: "", Reference: reference, Name: "", URI: "", RelationType: "",
		ID: uuid.Nil, SourceID: uuid.Nil, TargetID: uuid.Nil,
	}
}

// commandOutcome returns outcome with code and the text of cause.
// If cause is nil, commandOutcome leaves the failure code and message empty.
func commandOutcome(outcome audit.Outcome, code string, cause error) audit.OutcomeDetails {
	details := audit.OutcomeDetails{Outcome: outcome, FailureCode: "", FailureMessage: "", CommittedSteps: nil}
	if cause != nil {
		details.FailureCode, details.FailureMessage = code, cause.Error()
	}
	return details
}

// recordedStep saves the step intent, runs effect, then saves the step result.
// It calls observe for result evidence only after effect succeeds.
func recordedStep(
	ctx context.Context,
	steps audit.StepRecorder,
	step audit.Event,
	effect func(context.Context) error,
	observe func(context.Context) (json.RawMessage, error),
) (audit.OutcomeDetails, error) {
	if err := steps.BeginStep(ctx, step); err != nil {
		slog.ErrorContext(ctx, "ops.step_record_failed", slog.String("err", err.Error()))
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), fmt.Errorf("record the step: %w", err)
	}
	effectErr := effect(ctx)
	details := commandOutcome(audit.OutcomeCompleted, "", nil)
	var evidence json.RawMessage
	if effectErr != nil {
		details = commandOutcome(audit.OutcomeFailed, commandFailedCode, effectErr)
	} else {
		observed, err := observe(ctx)
		if err != nil {
			details = commandOutcome(audit.OutcomeIncomplete, commandFailedCode, err)
			effectErr = err
		}
		evidence = observed
	}
	if err := steps.FinishStep(ctx, audit.FinishedStep(step, details, evidence)); err != nil {
		slog.ErrorContext(ctx, "ops.step_record_failed", slog.String("err", err.Error()))
		return details, errors.Join(effectErr, fmt.Errorf("record the step result: %w", err))
	}
	return details, effectErr
}

type stepEvidence interface {
	isStepEvidence()
}

func encodeEvidence[Evidence stepEvidence](ctx context.Context, evidence Evidence) (json.RawMessage, error) {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		slog.ErrorContext(ctx, "ops.evidence_encode_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("encode step evidence: %w", err)
	}
	return encoded, nil
}
