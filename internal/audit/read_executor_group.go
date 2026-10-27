package audit

import (
	"context"
	"errors"
	"log/slog"
)

// ExecuteReadGroup saves one admission for each organization in the response.
// It runs read once and saves one final event for each admission before returning the payload.
// Each event records the same payload digest and content type. Each event includes only
// its organization's targets from ReadResult.OrgTargets and counts those targets.
// If any admission or finalization fails, or recovery replaces any owner,
// ExecuteReadGroup returns an error that wraps ErrReadNotRecorded without a payload.
func ExecuteReadGroup[T ReadValue](
	ctx context.Context,
	store ReadLifecycle,
	admissions []ReadAdmission,
	read func(context.Context) (ReadResult[T], error),
) (EncodedRead, error) {
	return executeReads(ctx, store, admissions, read,
		func(result ReadResult[T], admission ReadAdmission) ([]Target, uint64) {
			targets := result.OrgTargets[admission.Template.Context.OrgID]
			return targets, uint64(len(targets))
		})
}

// executeReads saves each admission, runs read once, and saves one final event per admission.
// It renews each lease while read runs. selectTargets returns the targets and count for each event.
func executeReads[T ReadValue](
	ctx context.Context,
	store ReadLifecycle,
	admissions []ReadAdmission,
	read func(context.Context) (ReadResult[T], error),
	selectTargets func(ReadResult[T], ReadAdmission) ([]Target, uint64),
) (EncodedRead, error) {
	if len(admissions) == 0 {
		err := errors.New("audit read: the read does not have an admission")
		slog.ErrorContext(ctx, "audit.read_executor.admission_missing", slog.String("err", err.Error()))
		return noEncodedRead(), errors.Join(ErrReadNotRecorded, err)
	}
	tokens := make([]ReadToken, 0, len(admissions))
	for _, admission := range admissions {
		token, err := admitRead(ctx, store, admission)
		if err != nil {
			return noEncodedRead(), err
		}
		tokens = append(tokens, token)
	}
	stops := make([]func() error, 0, len(tokens))
	for _, token := range tokens {
		stops = append(stops, KeepReadOwned(ctx, store, token))
	}
	result, readErr := read(ctx)
	prepared := prepareRead(result, readErr)
	for index, stop := range stops {
		if renewErr := stop(); renewErr != nil {
			slog.WarnContext(ctx, "audit.read_executor.renewal_failed",
				slog.String("event_id", tokens[index].EventID.String()), slog.String("err", renewErr.Error()))
		}
	}
	finalizeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), ReadFinalizeTimeout)
	defer cancel()
	evidence := prepared.encoded.Evidence
	for index, admission := range admissions {
		forAdmission := prepared
		if prepared.hasResult {
			forAdmission.targets, forAdmission.encoded.Evidence.Count = selectTargets(result, admission)
		}
		completion, err := readCompletion(tokens[index], admission.Template, forAdmission)
		if err != nil {
			return noEncodedRead(), notRecorded(ctx, tokens[index], err)
		}
		if err := finalizeRead(finalizeContext, store, completion); err != nil {
			return noEncodedRead(), notRecorded(ctx, tokens[index], err)
		}
		if index == 0 && completion.Event.Context.Read != nil {
			evidence = *completion.Event.Context.Read
		}
	}
	if readErr != nil {
		return noEncodedRead(), readErr
	}
	if prepared.encodeErr != nil {
		return noEncodedRead(), prepared.encodeErr
	}
	encoded := prepared.encoded
	encoded.Evidence = evidence
	return encoded, nil
}
