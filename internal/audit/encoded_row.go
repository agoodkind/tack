package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

type chainRowEncoding struct {
	result    chainAppendResult
	errorJSON []byte
}

func prepareChainRow(
	ctx context.Context,
	input chainAppendInput,
	head chainHeadState,
) (chainRowEncoding, error) {
	var empty chainRowEncoding
	lastHash := head.LastHash
	seq := head.LastSeq + 1

	rowHash, err := hashRowForEvent(rowHashInput{
		Event:       input.Event,
		EventID:     input.EventID,
		Shard:       input.Shard,
		Seq:         seq,
		PIIRef:      input.PIIRef,
		ContextJSON: input.ContextJSON,
		DeltaJSON:   input.DeltaJSON,
		LastHash:    lastHash,
		Version:     auditHashVersionCurrent,
	})
	if err != nil {
		slog.ErrorContext(ctx, "audit.chain.hash_failed", slog.String("err", err.Error()))

		return empty, fmt.Errorf("chain hash: %w", err)
	}

	// Encode an absent event error as nil bytes for SQL NULL. Audit readers
	// distinguish absent errors with IS NULL. The row hash above uses the
	// event before this column encoding.
	var errorJSON []byte
	if input.Event.Error != nil {
		encoded, err := json.Marshal(input.Event.Error)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"audit.chain.error_marshal_failed",
				slog.String("err", err.Error()),
			)

			return empty, fmt.Errorf("event error marshal: %w", err)
		}

		errorJSON = encoded
	}

	return chainRowEncoding{
		result:    chainAppendResult{Seq: seq, PrevHash: lastHash, RowHash: rowHash},
		errorJSON: errorJSON,
	}, nil
}
