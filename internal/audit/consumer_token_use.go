// consumer_token_use.go updates token last-use times from auth events in the
// same transaction as their ledger rows. The app does not update that column
// during requests (TACK-502).

package audit

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// recordTokenUses updates each token once with its latest accepted-auth time
// in the batch transaction. GREATEST prevents older or duplicate events from
// reducing an existing last-use time.
func recordTokenUses(ctx context.Context, tx pgx.Tx, projected []committedAuthEvent) error {
	newest := newestTokenUses(projected)
	for _, tokenID := range sortedTokenIDs(newest) {
		if _, err := tx.Exec(
			ctx,
			`
			UPDATE public.api_tokens
			SET last_used = GREATEST(last_used, $2::timestamptz)
			WHERE id = $1
		`,
			tokenID,
			newest[tokenID],
		); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.consumer.token_use_failed",
				slog.String("token_id", tokenID.String()),
				slog.String("err", err.Error()),
			)

			return fmt.Errorf("record token use %s: %w", tokenID, err)
		}
	}

	return nil
}

// newestTokenUses selects the latest accepted-auth time per non-nil token ID.
func newestTokenUses(projected []committedAuthEvent) map[uuid.UUID]time.Time {
	newest := map[uuid.UUID]time.Time{}

	for _, event := range projected {
		if event.Action != string(VerbAuthTokenUsed) || event.APITokenID == uuid.Nil {
			continue
		}

		current, seen := newest[event.APITokenID]
		if !seen || event.EventTime.After(current) {
			newest[event.APITokenID] = event.EventTime
		}
	}

	return newest
}

// sortedTokenIDs orders row updates consistently across batches that update
// overlapping token sets.
func sortedTokenIDs(newest map[uuid.UUID]time.Time) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(newest))
	for id := range newest {
		ids = append(ids, id)
	}

	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	return ids
}
