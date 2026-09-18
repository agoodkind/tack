// consumer_token_use.go projects each token's last use out of the auth events
// a batch has committed, so the app never writes the token table on the
// request path (TACK-502). The ledger row is the record; the token column is
// a projection of it, written in the same transaction.

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

// recordTokenUses writes the newest use time of every token the batch's auth
// events name, one update per token, inside the batch transaction. The update
// keeps whichever time is later, so an event delivered out of order or twice
// can never move a token's last use backwards.
func recordTokenUses(ctx context.Context, tx pgx.Tx, projected []projectedEvent) error {
	newest := newestTokenUses(projected)
	for _, tokenID := range sortedTokenIDs(newest) {
		if _, err := tx.Exec(ctx, `
			UPDATE api_tokens
			SET last_used = GREATEST(last_used, $2::timestamptz)
			WHERE id = $1
		`, tokenID, newest[tokenID]); err != nil {
			slog.ErrorContext(ctx, "audit.consumer.token_use_failed",
				slog.String("token_id", tokenID.String()), slog.String("err", err.Error()))
			return fmt.Errorf("record token use %s: %w", tokenID, err)
		}
	}
	return nil
}

// newestTokenUses folds a batch down to the latest accepted-auth time per
// token, skipping events that accepted no token.
func newestTokenUses(projected []projectedEvent) map[uuid.UUID]time.Time {
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

// sortedTokenIDs fixes the update order, so two batches that touch the same
// tokens lock their rows in the same sequence.
func sortedTokenIDs(newest map[uuid.UUID]time.Time) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(newest))
	for id := range newest {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	return ids
}
