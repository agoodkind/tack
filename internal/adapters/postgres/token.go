package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "github.com/agoodkind/tack/internal/domain"
	"github.com/agoodkind/tack/internal/domain/token"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type TokenRepo struct {
	db *pgxpool.Pool
}

func NewTokenRepo(db *pgxpool.Pool) *TokenRepo {
	return &TokenRepo{db: db}
}

func (r *TokenRepo) Validate(ctx context.Context, raw string) (*token.Token, error) {
	const q = `
		UPDATE api_tokens
		SET last_used_at = now()
		WHERE token = $1
		  AND is_active = true
		  AND (expires_at IS NULL OR expires_at > now())
		RETURNING id, user_id, workspace_id, label, is_active, last_used_at, expires_at, created_at`

	t := &token.Token{Token: raw}
	now := time.Now()
	err := r.db.QueryRow(ctx, q, raw).Scan(
		&t.ID, &t.UserID, &t.WorkspaceID, &t.Label,
		&t.IsActive, &now, &t.ExpiresAt, &t.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrUnauthenticated
		}
		return nil, fmt.Errorf("token validate: %w", err)
	}
	t.LastUsedAt = &now
	return t, nil
}

func (r *TokenRepo) Create(ctx context.Context, t *token.Token) (*token.Token, error) {
	const q = `
		INSERT INTO api_tokens (user_id, workspace_id, token, label, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`

	err := r.db.QueryRow(ctx, q,
		t.UserID, t.WorkspaceID, t.Token, t.Label, t.ExpiresAt,
	).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("token create: %w", pgErr(err))
	}
	return t, nil
}

func (r *TokenRepo) List(ctx context.Context, userID uuid.UUID) ([]*token.Token, error) {
	const q = `
		SELECT id, user_id, workspace_id, label, is_active, last_used_at, expires_at, created_at
		FROM api_tokens
		WHERE user_id = $1
		ORDER BY created_at DESC`

	rows, err := r.db.Query(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tokens []*token.Token
	for rows.Next() {
		t := &token.Token{}
		if err := rows.Scan(
			&t.ID, &t.UserID, &t.WorkspaceID, &t.Label,
			&t.IsActive, &t.LastUsedAt, &t.ExpiresAt, &t.CreatedAt,
		); err != nil {
			return nil, err
		}
		tokens = append(tokens, t)
	}
	return tokens, rows.Err()
}

func (r *TokenRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	const q = `UPDATE api_tokens SET is_active = false WHERE id = $1`
	_, err := r.db.Exec(ctx, q, id)
	return err
}
