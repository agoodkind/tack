// The consumer derives token last-use times from batch authentication events
// (TACK-502). The application request path does not write the token table.
// The ledger event supplies the record, and the consumer updates the token
// column in the same transaction.

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

// recordTokenUses updates each token once with its newest authentication
// time in the batch transaction. It keeps the later timestamp when events
// arrive out of order or repeat.
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

// newestTokenUses selects each token's latest accepted authentication time
// and skips events that did not accept a token.
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

// sortedTokenIDs orders token updates. Batches with overlapping tokens lock
// their rows in the same sequence.
func sortedTokenIDs(newest map[uuid.UUID]time.Time) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(newest))
	for id := range newest {
		ids = append(ids, id)
	}

	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })

	return ids
}
