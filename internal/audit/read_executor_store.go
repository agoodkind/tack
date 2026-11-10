package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
)

// readRetryInterval is the wait between two attempts to save an admission or a final event.
const readRetryInterval = 250 * time.Millisecond

// admitRead saves the admission and retries a failed attempt with the same
// identity until ReadAdmissionRetryWindow passes or ctx ends.
func admitRead(ctx context.Context, store ReadLifecycle, admission ReadAdmission) (ReadToken, error) {
	if err := ValidateReadAdmission(admission); err != nil {
		return ReadToken{}, notRecorded(ctx, admission.Token, err)
	}
	deadline := clock.Now().Add(ReadAdmissionRetryWindow)
	for {
		token, err := store.BeginRead(ctx, admission)
		if err == nil {
			return token, nil
		}
		if errors.Is(err, ErrReadAdmissionExpired) || errors.Is(err, ErrReadOwnerLost) || !waitToRetry(ctx, deadline) {
			return ReadToken{}, notRecorded(ctx, admission.Token, err)
		}
		slog.WarnContext(ctx, "audit.read_executor.admission_retry",
			slog.String("event_id", admission.Token.EventID.String()), slog.String("err", err.Error()))
	}
}

// finalizeRead saves the final event and retries failed attempts with the same bytes
// while ctx allows another attempt. It does not retry ErrReadOwnerLost or ErrReadIdentityChanged.
// The store uses the digest to confirm an earlier commit if its result is uncertain.
func finalizeRead(ctx context.Context, store ReadLifecycle, completion ReadCompletion) error {
	for {
		err := store.FinalizeRead(ctx, completion)
		if err == nil {
			return nil
		}
		err = fmt.Errorf("the server could not save the final event for audit read %s: %w", completion.Token.EventID, err)
		if errors.Is(err, ErrReadOwnerLost) || errors.Is(err, ErrReadIdentityChanged) {
			return err
		}
		deadline, _ := ctx.Deadline()
		if !waitToRetry(ctx, deadline) {
			return err
		}
		slog.WarnContext(ctx, "audit.read_executor.finalize_retry",
			slog.String("event_id", completion.Token.EventID.String()), slog.String("err", err.Error()))
	}
}

// waitToRetry waits readRetryInterval and reports whether another attempt
// can start before deadline and before ctx ends.
func waitToRetry(ctx context.Context, deadline time.Time) bool {
	if !deadline.IsZero() && clock.Now().Add(readRetryInterval).After(deadline) {
		return false
	}
	timer := time.NewTimer(readRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// readCompletion builds the final event from the admission template and prepared result.
// It computes the transport digest of the event and its targets.
// It includes the returned identities only when the read completes successfully.
func readCompletion(token ReadToken, template Event, prepared preparedRead) (ReadCompletion, error) {
	final := template
	final.Outcome = prepared.outcome.Outcome
	final.Context.CompletedAt = clock.Now().UTC()
	if final.Context.CompletedAt.Before(template.Context.StartedAt) {
		final.Context.CompletedAt = template.Context.StartedAt
	}
	final.OccurredAt = final.Context.CompletedAt
	final.Context.CommittedSteps = prepared.outcome.CommittedSteps
	final.Error = nil
	if final.Outcome != OutcomeCompleted {
		final.Error = &EventError{Code: prepared.outcome.FailureCode, Message: prepared.outcome.FailureMessage}
	}
	var targets []Target
	final.Context.Read = nil
	if prepared.hasResult {
		evidence := prepared.encoded.Evidence
		if final.Outcome != OutcomeCompleted {
			evidence.Count = 0
		}
		if final.Outcome == OutcomeCompleted && (evidence.Count > 0 || len(prepared.targets) > 0) {
			targets = prepared.targets
			digest, parts, err := ReadManifestDigest(targets)
			if err != nil {
				return ReadCompletion{}, err
			}
			evidence.ManifestSHA256, evidence.ManifestParts = digest, parts
		}
		final.Context.Read = &evidence
	}
	if err := ValidateEvent(final); err != nil {
		slog.Error("audit.read_executor.final_event_invalid",
			slog.String("event_id", final.EventID.String()), slog.String("err", err.Error()))
		return ReadCompletion{}, fmt.Errorf("the final read event failed validation: %w", err)
	}
	encoded, err := EncodeEvent(final, targets)
	if err != nil {
		return ReadCompletion{}, err
	}
	return ReadCompletion{Token: token, Event: final, Targets: targets, Digest: encoded.TransportDigest}, nil
}
