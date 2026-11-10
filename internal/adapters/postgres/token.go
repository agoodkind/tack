package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/token"
)

type TokenRepo struct {
	db *pgxpool.Pool
}

func NewTokenRepo(db *pgxpool.Pool) *TokenRepo {
	return &TokenRepo{db: db}
}

// Validate reads the token record for a raw Bearer token.
//
// The audit-consumer updates last_used from the auth event that the request
// records (TACK-502). Token validation does not update the token table.
//
//   - A missing or expired token returns [domain.ErrUnauthenticated].
func (r *TokenRepo) Validate(ctx context.Context, raw string) (*token.Token, error) {
	const q = `
		SELECT id, user_id, label, last_used, expires_at, created_at
		FROM api_tokens
		WHERE token_hash = $1
		  AND (expires_at IS NULL OR expires_at > now())`

	t := &token.Token{}
	err := r.db.QueryRow(ctx, q, hashToken(raw)).Scan(
		&t.ID,
		&t.UserID,
		&t.Label,
		&t.LastUsed,
		&t.ExpiresAt,
		&t.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrUnauthenticated
		}

		return nil, fmt.Errorf("token validate: %w", err)
	}

	return t, nil
}

func (r *TokenRepo) List(ctx context.Context, userID uuid.UUID) ([]*token.Token, error) {
	const q = `
		SELECT id, user_id, label, last_used, expires_at, created_at
		FROM api_tokens
		WHERE user_id = $1
		ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("token list: %w", err)
	}
	defer rows.Close()

	var tokens []*token.Token

	for rows.Next() {
		t := &token.Token{}
		if err := rows.Scan(
			&t.ID,
			&t.UserID,
			&t.Label,
			&t.LastUsed,
			&t.ExpiresAt,
			&t.CreatedAt,
		); err != nil {
			return nil, err
		}

		tokens = append(tokens, t)
	}

	return tokens, rows.Err()
}

// GetByID returns one token record with primary key id.
//
// It does not update last_used.
//
//   - A missing row returns [domain.ErrNotFound].
func (r *TokenRepo) GetByID(ctx context.Context, id uuid.UUID) (*token.Token, error) {
	const q = `
		SELECT id, user_id, label, last_used, expires_at, created_at
		FROM api_tokens
		WHERE id = $1`

	t := &token.Token{}
	err := r.db.QueryRow(ctx, q, id).Scan(
		&t.ID,
		&t.UserID,
		&t.Label,
		&t.LastUsed,
		&t.ExpiresAt,
		&t.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}

		slog.ErrorContext(
			ctx,
			"token.get_failed",
			slog.String("token_id", id.String()),
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("token get: %w", err)
	}

	return t, nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
