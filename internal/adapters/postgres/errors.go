package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	domain "goodkind.io/tack/internal/domain"
)

// pgErr translates pgx errors to domain sentinel errors.
// Raw DB errors must never surface above the postgres adapter.
func pgErr(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return domain.ErrAlreadyExists
		case "23503": // foreign_key_violation
			return domain.ErrInvalidArgument
		}
	}
	return err
}
