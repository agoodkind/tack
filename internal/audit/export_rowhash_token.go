package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
)

// contextWithZeroTokenID marshals an event context with "api_token_id" always
// present. The outer field shadows the embedded one, so encoding/json emits
// the key even though its value is the zero UUID.
type contextWithZeroTokenID struct {
	EventContext

	APITokenID uuid.UUID `json:"api_token_id"`
}

// checkZeroTokenIDRowHash is the second and last try for a version 3 row whose
// context holds no token id. From f48a5a6 (TACK-502) until TACK-514, the
// writer tagged EventContext.APITokenID omitempty, which encoding/json never
// applies to an array, so every event of that window hashed its context with
// "api_token_id" set to the zero UUID. A row decodes to the same context
// either way, and the current encoding omits the zero id, so those rows are
// recomputed here with the key restored. Rows written before and after that
// window, and every row with a real token id, match on the first try.
func checkZeroTokenIDRowHash(row Row) (bool, string, error) {
	if row.Context.APITokenID != uuid.Nil {
		return false, "hash mismatch", nil
	}
	input, err := rowHashInputFor(row)
	if err != nil {
		return false, "", err
	}
	contextJSON, err := json.Marshal(contextWithZeroTokenID{EventContext: row.Context, APITokenID: uuid.Nil})
	if err != nil {
		slog.Error("audit.export.row_context_encode_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return false, "", fmt.Errorf("verify row %s context with zero token id: %w", row.EventID, err)
	}
	input.ContextJSON = contextJSON
	expected, err := hashRowForEvent(input)
	if err != nil {
		slog.Error("audit.export.hash_failed", slog.String("event_id", row.EventID.String()), slog.String("err", err.Error()))
		return false, "", fmt.Errorf("verify hash row %s with zero token id: %w", row.EventID, err)
	}
	if bytesEqual(expected, row.RowHash) {
		return true, "", nil
	}
	return false, "hash mismatch", nil
}
