package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// contextWithZeroTokenID marshals an event context with an explicit
// api_token_id key. Its outer field shadows the embedded field, and
// encoding/json emits the key even for the zero UUID.
type contextWithZeroTokenID struct {
	EventContext

	APITokenID uuid.UUID `json:"api_token_id"`
}

// checkZeroTokenIDRowHash makes the final hash attempt for a version 3 row
// without a token ID. From f48a5a6 (TACK-502) until TACK-514, the writer
// applied omitempty to EventContext.APITokenID. encoding/json does not omit a
// zero UUID array under that option, and events without a token hashed an
// explicit zero api_token_id.
//
// Both encodings decode to the same context. The current encoder omits the zero
// ID, and this function restores the key to verify rows from that period. Rows
// from before or after that period and rows with real token IDs match the
// ordinary first computation.
func checkZeroTokenIDRowHash(row Row) (bool, string, error) {
	if row.Context.APITokenID != uuid.Nil {
		return false, "hash mismatch", nil
	}

	input, err := rowHashInputFor(row)
	if err != nil {
		return false, "", err
	}

	contextJSON, err := json.Marshal(contextWithZeroTokenID{
		EventContext: row.Context,
		APITokenID:   uuid.Nil,
	})
	if err != nil {
		slog.Error(
			"audit.export.row_context_encode_failed",
			slog.String("event_id", row.EventID.String()),
			slog.String("err", err.Error()),
		)

		return false, "", fmt.Errorf(
			"verify row %s context with zero token id: %w",
			row.EventID,
			err,
		)
	}

	input.ContextJSON = contextJSON
	expected, err := hashRowForEvent(input)
	if err != nil {
		slog.Error(
			"audit.export.hash_failed",
			slog.String("event_id", row.EventID.String()),
			slog.String("err", err.Error()),
		)

		return false, "", fmt.Errorf("verify hash row %s with zero token id: %w", row.EventID, err)
	}

	if bytesEqual(expected, row.RowHash) {
		return true, "", nil
	}

	return false, "hash mismatch", nil
}
