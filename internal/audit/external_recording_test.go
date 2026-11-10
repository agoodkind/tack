package audit_test

import (
	"context"
	"errors"
	"testing"

	"goodkind.io/tack/internal/audit"
)

// TestRunExternalMarksUnsavedIntent checks that RunExternal without an outbox
// does not run the effect and returns audit.ErrEventNotRecorded and
// audit.ErrExternalIntentNotSaved.
func TestRunExternalMarksUnsavedIntent(t *testing.T) {
	var intent audit.Event
	ran := false
	err := audit.RunExternal(
		t.Context(),
		nil,
		intent,
		func(context.Context, audit.StepRecorder) (audit.OutcomeDetails, error) {
			ran = true

			return audit.OutcomeDetails{
				Outcome:        audit.OutcomeCompleted,
				FailureCode:    "",
				FailureMessage: "",
				CommittedSteps: nil,
			}, nil
		},
	)
	if ran {
		t.Fatal("RunExternal ran the effect without a saved intent")
	}

	if !errors.Is(err, audit.ErrEventNotRecorded) ||
		!errors.Is(err, audit.ErrExternalIntentNotSaved) {
		t.Fatalf(
			"RunExternal without an outbox = %v, want %v and %v",
			err,
			audit.ErrEventNotRecorded,
			audit.ErrExternalIntentNotSaved,
		)
	}
}
