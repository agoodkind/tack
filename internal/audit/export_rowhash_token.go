package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// contextWithZeroTokenID always encodes api_token_id, including the zero UUID.
// The outer field overrides the embedded field's omission rule.
type contextWithZeroTokenID struct {
	EventContext

	APITokenID uuid.UUID `json:"api_token_id"`
}

// checkZeroTokenIDRowHash retries verification for version 3 rows with a zero
// APITokenID. Between f48a5a6 (TACK-502) and TACK-514, omitempty encoded the
// zero UUID array in every context. The current encoder omits it. This retry
// restores the historical key for hashing without changing decoded values.
// Rows outside that interval and rows with nonzero token IDs verify first.
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
