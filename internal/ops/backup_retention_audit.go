package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/audit"
)

type retentionEvidence struct {
	Bucket              string    `json:"bucket"`
	Target              string    `json:"target"`
	Action              string    `json:"action"`
	Cutoff              time.Time `json:"cutoff"`
	DeletedObjects      int       `json:"deleted_objects"`
	RequestedObjects    int       `json:"requested_objects"`
	Uncertain           bool      `json:"uncertain"`
	ExpireBeforeVersion int64     `json:"expire_before_version,omitempty"`
}

func (retentionEvidence) isStepEvidence() {}

type retentionPartialError struct{ cause error }

func (err *retentionPartialError) Error() string { return err.cause.Error() }
func (err *retentionPartialError) Unwrap() error { return err.cause }

func retentionStep(ctx context.Context, steps audit.StepRecorder, evidence retentionEvidence, effect func() (int, error)) error {
	encoded, err := encodeEvidence(ctx, evidence)
	if err != nil {
		return err
	}
	step := audit.ExternalStep(externalTarget("s3/"+evidence.Bucket+"/"+evidence.Target), encoded)
	if err := steps.BeginStep(ctx, step); err != nil {
		slog.ErrorContext(ctx, "backup.retention.step_intent_failed", slog.String("err", err.Error()))
		return fmt.Errorf("record retention step intent: %w", err)
	}
	count, effectErr := effect()
	evidence.DeletedObjects = count
	evidence.RequestedObjects = count
	var deletionError *retentionDeleteError
	if errors.As(effectErr, &deletionError) {
		evidence.RequestedObjects = deletionError.requested
		evidence.Uncertain = deletionError.uncertain
	}
	details := commandOutcome(audit.OutcomeCompleted, "", nil)
	if effectErr != nil {
		outcome := audit.OutcomeFailed
		if evidence.Uncertain {
			outcome = audit.OutcomeIncomplete
		}
		if count > 0 {
			outcome = audit.OutcomePartiallyCompleted
			effectErr = &retentionPartialError{cause: effectErr}
		}
		details = commandOutcome(outcome, commandFailedCode, effectErr)
		if count > 0 {
			details.CommittedSteps = []string{evidence.Target}
		}
	}
	encoded, encodeErr := encodeEvidence(ctx, evidence)
	if encodeErr != nil {
		effectErr = errors.Join(effectErr, encodeErr)
	}
	// Cancellation must not discard the observed effect from the audit outbox.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := steps.FinishStep(recordCtx, audit.FinishedStep(step, details, encoded)); err != nil {
		return &retentionPartialError{cause: errors.Join(effectErr, fmt.Errorf("record retention result: %w", err))}
	}
	return effectErr
}
