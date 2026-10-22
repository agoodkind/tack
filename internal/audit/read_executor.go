package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"
)

// ReadFinalizeTimeout limits the time that ExecuteRead can spend recording the final event.
const ReadFinalizeTimeout = 30 * time.Second

// readPayloadFormatDefault is the payload format of a result without a content type.
const readPayloadFormatDefault = "application/octet-stream"

// ErrReadNotRecorded reports that ExecuteRead could not save a read admission or final event.
// The caller must discard the result.
var ErrReadNotRecorded = errors.New("audit read: the server could not save the read event")

// ReadValue limits the result types that ExecuteRead encodes.
// A result type satisfies ReadValue by embedding ReadValueMarker.
type ReadValue interface {
	isAuditReadValue()
}

// ReadValueMarker provides the method that a result type needs to satisfy ReadValue.
type ReadValueMarker struct{}

func (ReadValueMarker) isAuditReadValue() {}

// ReadResult contains one prepared read result and the identities it returns.
// Encode writes Value as the response payload. A zero Outcome means the read completed.
type ReadResult[T ReadValue] struct {
	Value       T
	Targets     []Target
	Count       uint64
	Encode      func(io.Writer, T) error
	ContentType string
	Outcome     OutcomeDetails
}

// EncodedRead contains the response payload, its content type, and read evidence
// from the saved final event.
type EncodedRead struct {
	Payload     []byte
	ContentType string
	Evidence    ReadEvidence
}

// ExecuteRead saves the admission, runs read, encodes its result once while computing
// the SHA-256 digest, and saves the final event before returning the payload.
// It renews the lease while read runs. Final recording uses a ReadFinalizeTimeout
// deadline that does not depend on request cancellation.
// If admission or finalization fails, or recovery replaces the owner, ExecuteRead
// returns an error that wraps ErrReadNotRecorded and does not return a payload.
// If read returns an error, ExecuteRead saves a failed event and returns that error.
func ExecuteRead[T ReadValue](
	ctx context.Context,
	store ReadLifecycle,
	admission ReadAdmission,
	read func(context.Context) (ReadResult[T], error),
) (EncodedRead, error) {
	token, err := admitRead(ctx, store, admission)
	if err != nil {
		return EncodedRead{}, err
	}
	stopRenewal := KeepReadOwned(ctx, store, token)
	result, readErr := read(ctx)
	prepared := prepareRead(result, readErr)
	if renewErr := stopRenewal(); renewErr != nil {
		slog.WarnContext(ctx, "audit.read_executor.renewal_failed",
			slog.String("event_id", token.EventID.String()), slog.String("err", renewErr.Error()))
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), ReadFinalizeTimeout)
	defer cancel()
	completion, err := readCompletion(token, admission.Template, prepared)
	if err != nil {
		return EncodedRead{}, notRecorded(ctx, token, err)
	}
	if err := finalizeRead(finalizeContext, store, completion); err != nil {
		return EncodedRead{}, notRecorded(ctx, token, err)
	}
	if readErr != nil {
		return EncodedRead{}, readErr
	}
	if prepared.encodeErr != nil {
		return EncodedRead{}, prepared.encodeErr
	}
	encoded := prepared.encoded
	encoded.Evidence = *completion.Event.Context.Read
	return encoded, nil
}

// preparedRead contains the encoded payload, final outcome, and identities that one read returns.
type preparedRead struct {
	encoded   EncodedRead
	outcome   OutcomeDetails
	targets   []Target
	hasResult bool
	encodeErr error
}

// prepareRead encodes the result into one buffer while computing the payload digest.
// It returns a failed outcome without a payload if the read or encoding fails.
func prepareRead[T ReadValue](result ReadResult[T], readErr error) preparedRead {
	if readErr != nil {
		return preparedRead{
			encoded: noEncodedRead(),
			outcome: OutcomeDetails{
				Outcome: OutcomeFailed, FailureCode: "read_failed",
				FailureMessage: "the read failed", CommittedSteps: nil,
			},
			targets: nil, hasResult: false, encodeErr: nil,
		}
	}
	if result.Encode == nil {
		return failedEncoding(errors.New("audit read: the read result does not provide an encoder"))
	}
	var payload bytes.Buffer
	hasher := sha256.New()
	if err := result.Encode(io.MultiWriter(&payload, hasher), result.Value); err != nil {
		return failedEncoding(fmt.Errorf("audit read: the server could not encode the read result: %w", err))
	}
	format := result.ContentType
	if format == "" {
		format = readPayloadFormatDefault
	}
	outcome := result.Outcome
	if outcome.Outcome == "" {
		outcome.Outcome = OutcomeCompleted
	}
	evidence := ReadEvidence{
		Count: result.Count, PayloadSHA256: hex.EncodeToString(hasher.Sum(nil)), PayloadFormat: format,
		ManifestSHA256: "", ManifestParts: 0,
	}
	return preparedRead{
		encoded: EncodedRead{Payload: payload.Bytes(), ContentType: result.ContentType, Evidence: evidence},
		outcome: outcome, targets: result.Targets, hasResult: true, encodeErr: nil,
	}
}

// failedEncoding returns a failed outcome for an encoding error.
func failedEncoding(err error) preparedRead {
	slog.Error("audit.read_executor.encode_failed", slog.String("err", err.Error()))
	return preparedRead{
		encoded: noEncodedRead(),
		outcome: OutcomeDetails{
			Outcome: OutcomeFailed, FailureCode: "encode_failed",
			FailureMessage: "the server could not encode the read result", CommittedSteps: nil,
		},
		targets: nil, hasResult: false, encodeErr: err,
	}
}

// noEncodedRead returns an encoded read without a payload or evidence.
func noEncodedRead() EncodedRead {
	return EncodedRead{
		Payload: nil, ContentType: "",
		Evidence: ReadEvidence{Count: 0, PayloadSHA256: "", PayloadFormat: "", ManifestSHA256: "", ManifestParts: 0},
	}
}

// notRecorded logs a recording failure and wraps it with ErrReadNotRecorded.
func notRecorded(ctx context.Context, token ReadToken, err error) error {
	slog.ErrorContext(ctx, "audit.read_executor.not_recorded",
		slog.String("event_id", token.EventID.String()), slog.String("err", err.Error()))
	return fmt.Errorf("%w: read %s: %w", ErrReadNotRecorded, token.EventID, err)
}
