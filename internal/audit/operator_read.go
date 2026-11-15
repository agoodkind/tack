package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// ErrReadNotAdmitted reports that RecordOperatorRead did not save the
// admission and did not run the read.
var ErrReadNotAdmitted = errors.New(
	"audit read: RecordOperatorRead did not save the read admission",
)

// OperatorReadOutput contains the bytes the command prints.
type OperatorReadOutput struct {
	Payload []byte
}

// RecordOperatorRead saves the operator's attempt before running the read.
func RecordOperatorRead(
	ctx context.Context,
	store OutboxWriter,
	template Event,
	read func(context.Context) (OperatorReadOutput, error),
) ([]byte, error) {
	if store == nil {
		return nil, operatorReadFailure(
			ctx,
			"begin audit read",
			template,
			fmt.Errorf("%w: audit outbox is nil", ErrReadNotAdmitted),
		)
	}

	if err := ValidateEvent(template); err != nil {
		return nil, operatorReadFailure(
			ctx,
			"begin audit read",
			template,
			errors.Join(ErrReadNotAdmitted, err),
		)
	}

	if err := store.WriteOutbox(ctx, template); err != nil {
		return nil, operatorReadFailure(
			ctx,
			"begin audit read",
			template,
			errors.Join(ErrReadNotAdmitted, err),
		)
	}

	output, err := read(ctx)

	return output.Payload, err
}

func operatorReadFailure(ctx context.Context, action string, template Event, err error) error {
	wrapped := fmt.Errorf("%s %s: %w", action, template.EventID, err)
	slog.ErrorContext(
		ctx,
		"audit.operator_read.failed",
		slog.String("action", action),
		slog.String("verb", template.Verb),
		slog.String("event_id", template.EventID.String()),
		slog.String("err", wrapped.Error()),
	)

	return wrapped
}
