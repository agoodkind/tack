package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
)

// ValidateWithOrgs reads the token record for the supplied raw Bearer token
// and its user's organization set in one statement. It selects the record
// with hashToken(raw).
//
// This combined lookup requires one ledger read instead of two for an
// authenticated request with empty caches, as criterion 12 of the backup
// acceptance requires (TACK-504).
//
//   - A missing or expired token returns [domain.ErrUnauthenticated].
//   - A successful lookup for a user without memberships returns an empty
//     organization set instead of nil.
func (r *TokenRepo) ValidateWithOrgs(
	ctx context.Context,
	raw string,
) (*token.Token, []uuid.UUID, error) {
	const q = `
		SELECT t.id, t.user_id, t.label, t.last_used, t.expires_at, t.created_at,
		       COALESCE(array_agg(DISTINCT m.org_id) FILTER (WHERE m.org_id IS NOT NULL), '{}')
		FROM api_tokens t
		LEFT JOIN org_members m ON m.user_id = t.user_id
		WHERE t.token_hash = $1
		  AND (t.expires_at IS NULL OR t.expires_at > now())
		GROUP BY t.id, t.user_id, t.label, t.last_used, t.expires_at, t.created_at`

	record := &token.Token{
		ID:        uuid.Nil,
		UserID:    uuid.Nil,
		Label:     "",
		LastUsed:  nil,
		ExpiresAt: nil,
		CreatedAt: time.Time{},
	}
	var orgIDs []uuid.UUID
	err := r.db.QueryRow(ctx, q, hashToken(raw)).Scan(
		&record.ID,
		&record.UserID,
		&record.Label,
		&record.LastUsed,
		&record.ExpiresAt,
		&record.CreatedAt,
		&orgIDs,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, domain.ErrUnauthenticated
		}

		slog.ErrorContext(ctx, "token.validate_with_orgs_failed", slog.String("err", err.Error()))

		return nil, nil, fmt.Errorf("token validate with orgs: %w", err)
	}

	if orgIDs == nil {
		orgIDs = []uuid.UUID{}
	}

	return record, orgIDs, nil
}
