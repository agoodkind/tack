package audit

import (
	"slices"
	"strings"
)

const (
	// externalStepUnfinishedCode is the error code of an attempt that began a
	// step and did not save the result of that step.
	externalStepUnfinishedCode = "step_unfinished"
	// externalOutcomeUnknownCode is the error code of an attempt that returned
	// an outcome that is not a terminal outcome.
	externalOutcomeUnknownCode = "outcome_unknown"
)

// resolveExternalOutcome returns the terminal outcome of one attempt from the
// outcome that the run function returned, its error, and the recorded steps.
//
// A run error turns an empty or completed outcome into failed. A step that
// began without a saved result makes the outcome incomplete. A failed or
// refused outcome becomes partially_completed when a step committed.
// Only partially_completed and incomplete outcomes list committed steps.
func resolveExternalOutcome(details OutcomeDetails, runErr error, steps stepSummary) OutcomeDetails {
	resolved := details
	if runErr != nil && (resolved.Outcome == "" || resolved.Outcome == OutcomeCompleted) {
		resolved.Outcome = OutcomeFailed
	}
	if resolved.Outcome == "" {
		resolved.Outcome = OutcomeCompleted
	}
	if !isTerminalOutcome(resolved.Outcome) {
		resolved.Outcome = OutcomeIncomplete
		resolved.FailureCode = externalOutcomeUnknownCode
		resolved.FailureMessage = "the operation returned outcome " + string(details.Outcome) + ", which is not a terminal outcome"
	}
	if len(steps.unfinished) > 0 && resolved.Outcome != OutcomeIncomplete {
		resolved.Outcome = OutcomeIncomplete
		resolved.FailureCode = externalStepUnfinishedCode
		resolved.FailureMessage = "these steps began without a saved result: " + strings.Join(steps.unfinished, ", ")
	}
	committed := slices.Clone(details.CommittedSteps)
	for _, step := range steps.committed {
		if !slices.Contains(committed, step) {
			committed = append(committed, step)
		}
	}
	switch resolved.Outcome {
	case OutcomeFailed, OutcomeRefused:
		if len(committed) > 0 {
			resolved.Outcome = OutcomePartiallyCompleted
		}
	case OutcomePartiallyCompleted:
		if len(committed) == 0 {
			resolved.Outcome = OutcomeFailed
		}
	case OutcomeCompleted, OutcomeIncomplete, OutcomeOK, OutcomeError, OutcomePending, OutcomeUnrecorded:
	}
	resolved.CommittedSteps = nil
	if resolved.Outcome == OutcomePartiallyCompleted || resolved.Outcome == OutcomeIncomplete {
		resolved.CommittedSteps = committed
	}
	if resolved.Outcome == OutcomeCompleted {
		resolved.FailureCode, resolved.FailureMessage = "", ""
		return resolved
	}
	if resolved.FailureCode == "" {
		resolved.FailureCode = "external_" + string(resolved.Outcome)
	}
	if resolved.FailureMessage == "" && runErr != nil {
		resolved.FailureMessage = runErr.Error()
	}
	if resolved.FailureMessage == "" {
		resolved.FailureMessage = "the operation ended with outcome " + string(resolved.Outcome)
	}
	return resolved
}
