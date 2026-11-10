package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/audit"
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
		if err == nil || !errors.As(err, &postgresError) || postgresError.Code != serializationFailureSQLState {
			return states, err
		}
	}
	return nil, err
}

// finishRoleStep saves the observed role state and marks the step completed
// when seed-roles succeeds or the role state changes. If seed-roles fails
// without changing the role state, it marks the step incomplete. The role
// state does not reveal whether seed-roles changed the password.
func finishRoleStep(
	ctx context.Context,
	steps audit.StepRecorder,
	step audit.Event,
	role seededRole,
	before, after roleState,
	runErr error,
) error {
	changed := before.Exists != after.Exists || before.Login != after.Login || !slices.Equal(before.MemberOf, after.MemberOf)
	details := commandOutcome(audit.OutcomeCompleted, "", nil)
	passwordSet := runErr == nil
	if runErr != nil && !changed {
		details = commandOutcome(audit.OutcomeIncomplete, roleChangeUnknownCode,
			errors.New("seed-roles failed and the role attributes and memberships did not change"))
	}
	observed := after
	evidence, err := encodeEvidence(ctx, roleChangeEvidence{
		Role: role.login, BaseRole: role.base, Before: before, After: &observed, PasswordSet: passwordSet,
	})
	if err != nil {
		return err
	}
	if err := steps.FinishStep(ctx, audit.FinishedStep(step, details, evidence)); err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.step_record_failed", slog.String("err", err.Error()))
		return fmt.Errorf("audit seed-roles: record the result for role %s: %w", role.login, err)
	}
	return nil
}
