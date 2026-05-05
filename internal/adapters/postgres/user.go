package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/user"
)

type UserRepo struct {
	db *pgxpool.Pool
}

func NewUserRepo(db *pgxpool.Pool) *UserRepo {
	return &UserRepo{db: db}
}

func (r *UserRepo) Create(ctx context.Context, u *user.User) (*user.User, error) {
	// When the caller leaves u.ID zero, defer to the SQL DEFAULT (gen_random_uuid).
	// When the caller pre-sets u.ID, INSERT the explicit value. Used by the seed
	// path to make the dev-mode bearer (= raw user UUID) stable across wipes.
	if u.ID == uuid.Nil {
		const q = `
			INSERT INTO users (email, display_name)
			VALUES ($1, $2)
			RETURNING id, created_at, updated_at`
		err := r.db.QueryRow(ctx, q, u.Email, u.DisplayName).
			Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
		if err != nil {
			return nil, fmt.Errorf("user create: %w", pgErr(err))
		}
		return u, nil
	}
	const q = `
		INSERT INTO users (id, email, display_name)
		VALUES ($1, $2, $3)
		RETURNING id, created_at, updated_at`
	err := r.db.QueryRow(ctx, q, u.ID, u.Email, u.DisplayName).
		Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("user create: %w", pgErr(err))
	}
	return u, nil
}

func (r *UserRepo) GetByID(ctx context.Context, id uuid.UUID) (*user.User, error) {
	const q = `SELECT id, email, display_name, created_at, updated_at FROM users WHERE id = $1 AND is_active = true`
	u := &user.User{}
	err := r.db.QueryRow(ctx, q, id).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("user get: %w", err)
	}
	return u, nil
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*user.User, error) {
	const q = `SELECT id, email, display_name, created_at, updated_at FROM users WHERE email = $1 AND is_active = true`
	u := &user.User{}
	err := r.db.QueryRow(ctx, q, email).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		return nil, fmt.Errorf("user get by email: %w", err)
	}
	return u, nil
}
