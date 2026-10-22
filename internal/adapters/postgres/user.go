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

// Create inserts u and writes its audit change event to the outbox in one
// transaction. It reads the mutation operation from ctx and records the change
// in the system organization. It keeps a supplied ID or assigns a UUIDv7 when
// u.ID is [uuid.Nil]. The event uses protected references for email and display
// name in After and supplies their JSON values in Protected.
func (r *UserRepo) Create(ctx context.Context, u *user.User) (*user.User, error) {
	if u.ID == uuid.Nil {
		u.ID = uuid.Must(uuid.NewV7())
	}
	operation, err := changeOperation(ctx, audit.VerbUserCreate, audit.SystemOrgID())
	if err != nil {
		return nil, err
	}
	target := UserTarget(u.ID)
	err = changeTx(ctx, r.db, operation, target, func(tx pgx.Tx) error {
		const q = `
			INSERT INTO users (id, email, display_name)
			VALUES ($1, $2, $3)
			RETURNING created_at, updated_at, is_active`
		var createdAt, updatedAt time.Time
		var isActive bool
		if err := tx.QueryRow(ctx, q, u.ID, u.Email, u.DisplayName).Scan(&createdAt, &updatedAt, &isActive); err != nil {
			return pgErr(err)
		}
		u.CreatedAt, u.UpdatedAt = createdAt, updatedAt
		stored, protected, err := newUserValue(ctx, u, isActive)
		if err != nil {
			return err
		}
		after, err := changeValue(ctx, stored)
		if err != nil {
			return err
		}
		change, err := newChange(ctx, target, nil, after)
		if err != nil {
			return err
		}
		change.Protected = protected
		return WriteChanges(ctx, tx, operation, []audit.Change{change})
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
