package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"goodkind.io/tack/internal/audit"
	domain "goodkind.io/tack/internal/domain"
	"goodkind.io/tack/internal/domain/user"
)

type UserRepo struct {
	db *pgxpool.Pool
}

func NewUserRepo(db *pgxpool.Pool) *UserRepo {
	return &UserRepo{db: db}
}

// userEmailField and userDisplayNameField define the Changed entries for the
// two personal-data fields. User change events list these entries but do not
// store the field values.
const (
	userEmailField       = "/email"
	userDisplayNameField = "/display_name"
)

// userValue contains user state and timestamps for an audit change. It omits
// email and display name because they contain personal data.
type userValue struct {
	ID        uuid.UUID `json:"id"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Create inserts u and one audit change event in one transaction. It keeps a
// supplied ID or assigns a UUIDv7 when u.ID is [uuid.Nil]. The seed supplies a
// fixed ID, and the development bearer equals that ID across database wipes.
// The change event lists /email and /display_name in Changed but does not store
// either value because both contain personal data.
func (r *UserRepo) Create(ctx context.Context, operation audit.Event, u *user.User) (*user.User, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.Must(uuid.NewV7())
	}
	target := UserTarget(u.ID)
	err := changeTx(ctx, r.db, operation, target, func(tx pgx.Tx) error {
		const q = `
			INSERT INTO users (id, email, display_name)
			VALUES ($1, $2, $3)
			RETURNING created_at, updated_at, is_active`
		stored := userValue{ID: u.ID, IsActive: false, CreatedAt: time.Time{}, UpdatedAt: time.Time{}}
		if err := tx.QueryRow(ctx, q, u.ID, u.Email, u.DisplayName).
			Scan(&stored.CreatedAt, &stored.UpdatedAt, &stored.IsActive); err != nil {
			return pgErr(err)
		}
		u.CreatedAt, u.UpdatedAt = stored.CreatedAt, stored.UpdatedAt
		after, err := changeValue(ctx, &stored)
		if err != nil {
			return err
		}
		return WriteChanges(ctx, tx, operation, []audit.Change{{
			EventID: uuid.Must(uuid.NewV7()), Target: target, Before: nil, After: after,
			Changed: []string{userEmailField, userDisplayNameField},
		}})
	})
	if err != nil {
		slog.ErrorContext(ctx, "user.create_failed", slog.String("user_id", u.ID.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("create user %s: %w", u.ID, err)
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

// GetByIDIncludingInactive returns a user whether or not the account is
// active. Revoking a credential must reach a deactivated holder, because a
// token keeps authenticating after its user is deactivated.
func (r *UserRepo) GetByIDIncludingInactive(ctx context.Context, id uuid.UUID) (*user.User, error) {
	const q = `SELECT id, email, display_name, created_at, updated_at FROM users WHERE id = $1`
	u := &user.User{}
	err := r.db.QueryRow(ctx, q, id).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		slog.ErrorContext(ctx, "user.get_failed", slog.String("user_id", id.String()), slog.String("err", err.Error()))
		return nil, fmt.Errorf("user get including inactive: %w", err)
	}
	return u, nil
}

// GetByEmailIncludingInactive returns a user by exact email whether or not
// the account is active, so a deactivated user's credentials can be listed
// and revoked.
func (r *UserRepo) GetByEmailIncludingInactive(ctx context.Context, email string) (*user.User, error) {
	const q = `SELECT id, email, display_name, created_at, updated_at FROM users WHERE email = $1`
	u := &user.User{}
	err := r.db.QueryRow(ctx, q, email).Scan(&u.ID, &u.Email, &u.DisplayName, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNotFound
		}
		slog.ErrorContext(ctx, "user.get_by_email_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("user get by email including inactive: %w", err)
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
