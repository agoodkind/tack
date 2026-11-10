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

	// An action without an error stores SQL NULL. The nullable column permits
	// IS NULL checks for absent errors; a JSON null does not satisfy those checks.
	// hashRowForEvent hashes the typed event before column encoding. The column's
	// representation of an absent error does not change that hash.
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
