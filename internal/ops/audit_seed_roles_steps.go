package ops

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	roleStateReadAttempts = 3
	// serializationFailureSQLState is the SQLSTATE YugabyteDB returned when
	// it rejected the role-state query's catalog snapshot just after
	// seed-roles changed roles.
	serializationFailureSQLState = "40001"
)

// readSeededRoleStates returns the observed state of each seeded login role.
// It repeats the call to queryRoleStates only for a PostgreSQL error with
// SQLSTATE 40001. It returns immediately on success or any other error.
func readSeededRoleStates(ctx context.Context, pool *pgxpool.Pool) (map[string]roleState, error) {
	var err error

	for range roleStateReadAttempts {
		var states map[string]roleState
		states, err = queryRoleStates(ctx, pool)
		var postgresError *pgconn.PgError
		if err == nil || !errors.As(err, &postgresError) ||
			postgresError.Code != serializationFailureSQLState {
			return states, err
		}
	}

	return nil, err
}
