package ops

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/telemetry"
)

const invalidPasswordSQLState = "28P01"

type operatorPoolError struct {
	stage string
	cause error
}

func (e *operatorPoolError) Error() string { return e.stage }

func (e *operatorPoolError) Unwrap() error { return e.cause }

func prepareSeedRolesAuditOutbox(ctx context.Context, f *cli.Factory) error {
	if f.Cfg.AuditOperatorDSN == "" {
		return errors.New("audit seed-roles: set AUDIT_OPERATOR_DSN or AUDIT_OPERATOR_DSN_FILE")
	}
	if f.Cfg.AuditOperatorPassword == "" {
		return errors.New("audit seed-roles: set AUDIT_OPERATOR_PASSWORD or AUDIT_OPERATOR_PASSWORD_FILE")
	}
	if !declaredOperatorPassword(f.Cfg.AuditOperatorDSN) {
		return errors.New("audit seed-roles: AUDIT_OPERATOR_DSN must include the current password")
	}
	current, err := pgxpool.ParseConfig(f.Cfg.AuditOperatorDSN)
	if err != nil {
		return errors.New("audit seed-roles: invalid AUDIT_OPERATOR_DSN")
	}
	current.ConnConfig.Tracer = &telemetry.QueryTracer{}
	pool, err := authenticatedOperatorPool(ctx, current)
	if err != nil {
		if !onlyPasswordRejections(err) {
			return operatorConnectionError("current", err)
		}
		proposed := current.Copy()
		proposed.ConnConfig.Password = f.Cfg.AuditOperatorPassword
		pool, err = authenticatedOperatorPool(ctx, proposed)
		if err != nil {
			return operatorConnectionError("proposed", err)
		}
	}
	f.CloseAuditOutbox()
	f.SetAuditOutbox(audit.NewPoolOutbox(pool))
	f.SetAuditReads(audit.NewPoolReadLifecycle(pool))
	return nil
}

func authenticatedOperatorPool(ctx context.Context, config *pgxpool.Config) (*pgxpool.Pool, error) {
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, &operatorPoolError{stage: "open operator pool", cause: err}
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, &operatorPoolError{stage: "authenticate operator pool", cause: err}
	}
	return pool, nil
}

func onlyPasswordRejections(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !onlyPasswordRejections(child) {
				return false
			}
		}
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyPasswordRejections(wrapped.Unwrap())
	}
	var pgError *pgconn.PgError
	return errors.As(err, &pgError) && pgError.Code == invalidPasswordSQLState
}

func operatorConnectionError(candidate string, err error) error {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return fmt.Errorf("audit seed-roles: %s operator connection failed (SQLSTATE %s)", candidate, pgError.Code)
	}
	return fmt.Errorf("audit seed-roles: %s operator connection failed; check connection and TLS configuration", candidate)
}
