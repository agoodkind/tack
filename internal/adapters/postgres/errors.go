package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	domain "goodkind.io/tack/internal/domain"
)

// pgErr converts recognized pgx errors to domain sentinel errors, which
// callers can compare directly. The postgres adapter must not return raw
// database errors to higher layers.
func pgErr(err error) error {
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // SQLSTATE 23505 identifies a unique_violation error.
			return domain.ErrAlreadyExists
		case "23503": // SQLSTATE 23503 identifies a foreign_key_violation error.
			return domain.ErrInvalidArgument
		}
	}

	return err
}
