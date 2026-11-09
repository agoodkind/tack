package ops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres/retry"
	"goodkind.io/tack/internal/audit"
)

var errSeededRoleStateUnchanged = errors.New(
	"seed-roles failed and the role attributes and memberships did not change",
)

// readSeededRoleStates returns the observed state of each seeded login role.
// It retries a database transaction abort through retry.Run.
func readSeededRoleStates(ctx context.Context, pool *pgxpool.Pool) (map[string]roleState, error) {
	var states map[string]roleState
	err := retry.Run(
		ctx,
		func(ctx context.Context) error {
			var readErr error
			states, readErr = queryRoleStates(ctx, pool)

			return readErr
		},
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.seed_roles.state_retry_failed",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("audit seed-roles: read role states after retries: %w", err)
	}

	return states, nil
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
	before roleState,
	after roleState,
	runErr error,
) error {
	changed := before.Exists != after.Exists || before.Login != after.Login ||
		!slices.Equal(before.MemberOf, after.MemberOf)
	details := commandOutcome(audit.OutcomeCompleted, "", nil)
	passwordSet := runErr == nil
	if runErr != nil && !changed {
		details = commandOutcome(
			audit.OutcomeIncomplete,
			roleChangeUnknownCode,
			errSeededRoleStateUnchanged,
		)
	}

	observed := after
	evidence, err := encodeEvidence(
		ctx,
		roleChangeEvidence{
			Role:        role.login,
			BaseRole:    role.base,
			Before:      before,
			After:       &observed,
			PasswordSet: passwordSet,
		},
	)
	if err != nil {
		return err
	}

	if err := steps.FinishStep(ctx, audit.FinishedStep(step, details, evidence)); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.seed_roles.step_record_failed",
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("audit seed-roles: record the result for role %s: %w", role.login, err)
	}

	return nil
}
