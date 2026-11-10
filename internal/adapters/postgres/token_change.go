package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"goodkind.io/tack/internal/audit"
	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
)

// tokenValue stores token metadata for an audit change. It omits the raw
// token and its hash.
type tokenValue struct {
	ID        uuid.UUID  `json:"id"`
	UserID    uuid.UUID  `json:"user_id"`
	Label     string     `json:"label"`
	ExpiresAt *time.Time `json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

func newTokenValue(apiToken *token.Token) *tokenValue {
	return &tokenValue{
		ID:        apiToken.ID,
		UserID:    apiToken.UserID,
		Label:     apiToken.Label,
		ExpiresAt: apiToken.ExpiresAt,
		CreatedAt: apiToken.CreatedAt,
	}
}

func emptyToken() *token.Token {
	return &token.Token{
		ID:        uuid.Nil,
		UserID:    uuid.Nil,
		Label:     "",
		LastUsed:  nil,
		ExpiresAt: nil,
		CreatedAt: time.Time{},
	}
}

// Create stores the SHA-256 hash of raw under a new UUIDv7 ID.
//
// It writes the creation event in the same transaction as the token record.
//
//   - It does not store raw.
func (r *TokenRepo) Create(
	ctx context.Context,
	orgID uuid.UUID,
	userID uuid.UUID,
	raw string,
	label string,
) (*token.Token, error) {
	return r.insertToken(ctx, orgID, uuid.Must(uuid.NewV7()), userID, raw, label, nil)
}

// CreateWithID stores the SHA-256 hash of raw under the caller's chosen ID.
//
// It writes the creation event in the same transaction as the token record.
//
//   - It does not store raw.
func (r *TokenRepo) CreateWithID(
	ctx context.Context,
	orgID uuid.UUID,
	id uuid.UUID,
	userID uuid.UUID,
	raw string,
	label string,
) (*token.Token, error) {
	return r.insertToken(ctx, orgID, id, userID, raw, label, nil)
}

// CreateExpiring stores the SHA-256 hash of raw under a new UUIDv7 ID with
// expiresAt as its expiry.
//
// It writes the creation event in the same transaction as the token record.
//
//   - It does not store raw.
func (r *TokenRepo) CreateExpiring(
	ctx context.Context,
	orgID uuid.UUID,
	userID uuid.UUID,
	raw string,
	label string,
	expiresAt time.Time,
) (*token.Token, error) {
	return r.insertToken(ctx, orgID, uuid.Must(uuid.NewV7()), userID, raw, label, &expiresAt)
}

func (r *TokenRepo) insertToken(
	ctx context.Context,
	orgID uuid.UUID,
	id uuid.UUID,
	userID uuid.UUID,
	raw string,
	label string,
	expiresAt *time.Time,
) (*token.Token, error) {
	const q = `
		INSERT INTO api_tokens (id, user_id, token_hash, label, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, user_id, label, last_used, expires_at, created_at`

	operation, err := changeOperation(ctx, audit.VerbAuthTokenCreate, orgID)
	if err != nil {
		return nil, err
	}

	target := TokenTarget(id, label)
	t := emptyToken()
	err = changeTx(
		ctx,
		r.db,
		operation,
		target,
		func(tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, q, id, userID, hashToken(raw), label, expiresAt).Scan(
				&t.ID,
				&t.UserID,
				&t.Label,
				&t.LastUsed,
				&t.ExpiresAt,
				&t.CreatedAt,
			); err != nil {
				return pgErr(err)
			}

			after, err := changeValue(ctx, newTokenValue(t))
			if err != nil {
				return err
			}

			change, err := newChange(ctx, target, nil, after)
			if err != nil {
				return err
			}

			return WriteChanges(ctx, tx, operation, []audit.Change{change})
		},
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"token.create_failed",
			slog.String("token_id", id.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("create token %s: %w", id, err)
	}

	return t, nil
}

// Delete removes a token and records its old value in the same transaction.
//
// The old value contains the token metadata from the deleted row.
//
//   - A missing token returns an error wrapping [domain.ErrNotFound].
func (r *TokenRepo) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	const q = `
		DELETE FROM api_tokens WHERE id = $1
		RETURNING id, user_id, label, last_used, expires_at, created_at`

	operation, err := changeOperation(ctx, audit.VerbAuthTokenRevoke, orgID)
	if err != nil {
		return err
	}

	target := TokenTarget(id, "")
	err = changeTx(
		ctx,
		r.db,
		operation,
		target,
		func(tx pgx.Tx) error {
			t := emptyToken()
			if err := tx.QueryRow(ctx, q, id).Scan(
				&t.ID,
				&t.UserID,
				&t.Label,
				&t.LastUsed,
				&t.ExpiresAt,
				&t.CreatedAt,
			); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return domain.ErrNotFound
				}

				return pgErr(err)
			}

			before, err := changeValue(ctx, newTokenValue(t))
			if err != nil {
				return err
			}

			change, err := newChange(ctx, TokenTarget(id, t.Label), before, nil)
			if err != nil {
				return err
			}

			return WriteChanges(ctx, tx, operation, []audit.Change{change})
		},
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"token.delete_failed",
			slog.String("token_id", id.String()),
			slog.String("err", err.Error()),
		)

		return fmt.Errorf("delete token %s: %w", id, err)
	}

	return nil
}
