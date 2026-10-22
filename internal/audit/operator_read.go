package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/tack/internal/clock"
)

// ReadFinalizeDeadline limits how long RecordOperatorRead waits to save the
// final read event after the read returns.
const ReadFinalizeDeadline = 30 * time.Second

// readFailedCode is the error code of a read that returned an error.
const readFailedCode = "read_failed"

// ErrReadNotAdmitted reports that RecordOperatorRead did not save the
// admission and did not run the read.
var ErrReadNotAdmitted = errors.New("audit read: RecordOperatorRead did not save the read admission")

// OperatorReadOutput contains the exact bytes an operator read returns, their
// media type, and the identities of the returned objects in order.
type OperatorReadOutput struct {
	Payload []byte
	Format  string
	Targets []Target
}

// RecordOperatorRead saves template in store to admit the read, then calls
// read and renews its lease while it runs. It saves a final event with the
// SHA-256 digest of the exact payload bytes and the returned identities.
// It returns the payload only after store saves the final event. If read
// returns an error, RecordOperatorRead saves a failed event and returns that error.
func RecordOperatorRead(
	ctx context.Context,
	store ReadLifecycle,
	template Event,
	read func(context.Context) (OperatorReadOutput, error),
) ([]byte, error) {
	if store == nil {
		return nil, operatorReadFailure(ctx, "begin audit read", template,
			errors.Join(ErrReadNotAdmitted, errors.New("audit read store is nil")))
	}
	admission := ReadAdmission{
		Token:    ReadToken{EventID: template.EventID, Owner: NewReadOwner(), Generation: 0, AdmissionDeadline: time.Time{}},
		Template: template,
	}
	token, err := store.BeginRead(ctx, admission)
	if err != nil {
		return nil, operatorReadFailure(ctx, "begin audit read", template, errors.Join(ErrReadNotAdmitted, err))
	}
	stopRenewal := KeepReadOwned(ctx, store, token)
	output, readErr := read(ctx)
	renewErr := stopRenewal()
	completion, err := operatorReadCompletion(token, template, output, readErr)
	if err != nil {
		return nil, operatorReadFailure(ctx, "finalize audit read", template, err)
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), ReadFinalizeDeadline)
	defer cancel()
	if err := store.FinalizeRead(finalizeContext, completion); err != nil {
		return nil, operatorReadFailure(ctx, "finalize audit read", template, errors.Join(err, renewErr))
	}
	if readErr != nil {
		return nil, readErr
	}
	return output.Payload, nil
}

// operatorReadCompletion builds the final event of one read and its
// transport digest.
func operatorReadCompletion(token ReadToken, template Event, output OperatorReadOutput, readErr error) (ReadCompletion, error) {
	final := template
	final.Context.CompletedAt = clock.Now().UTC()
	if final.Context.CompletedAt.Before(final.Context.StartedAt) {
		final.Context.CompletedAt = final.Context.StartedAt
	}
	final.OccurredAt = template.OccurredAt
	targets := output.Targets
	if readErr != nil {
		final.Outcome = OutcomeFailed
		final.Error = &EventError{Code: readFailedCode, Message: readErr.Error()}
		final.Context.Read = nil
		targets = nil
	} else {
		evidence, err := operatorReadEvidence(output)
		if err != nil {
			return ReadCompletion{}, err
		}
		final.Outcome = OutcomeCompleted
		final.Error = nil
		final.Context.Read = &evidence
	}
	encoded, err := EncodeEvent(final, targets)
	if err != nil {
		return ReadCompletion{}, err
	}
	return ReadCompletion{Token: token, Event: final, Targets: targets, Digest: encoded.TransportDigest}, nil
}

// operatorReadEvidence returns the payload digest, format, count, and
// manifest digest of output.
func operatorReadEvidence(output OperatorReadOutput) (ReadEvidence, error) {
	evidence := ReadEvidence{
		Count: uint64(len(output.Targets)), PayloadSHA256: sha256Hex(output.Payload), PayloadFormat: output.Format,
		ManifestSHA256: "", ManifestParts: 0,
	}
	if len(output.Targets) == 0 {
		return evidence, nil
	}
	digest, parts, err := ReadManifestDigest(output.Targets)
	if err != nil {
		return ReadEvidence{}, err
	}
	evidence.ManifestSHA256, evidence.ManifestParts = digest, parts
	return evidence, nil
}

// operatorReadFailure logs one failed operator read transition and wraps err
// with the action and event ID.
func operatorReadFailure(ctx context.Context, action string, template Event, err error) error {
	wrapped := fmt.Errorf("%s %s: %w", action, template.EventID, err)
	slog.ErrorContext(ctx, "audit.operator_read.failed",
		slog.String("action", action), slog.String("verb", template.Verb),
		slog.String("event_id", template.EventID.String()), slog.String("err", wrapped.Error()))
	return wrapped
}
