package ops

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/telemetry"
)

// seededRoleStateQuery reads the login attribute and the audit base role
// memberships of the seeded login roles.
//
//go:embed audit_seed_roles_state.sql
var seededRoleStateQuery string

// seededRole pairs a login role with its optional base role.
// seed-roles sets LOGIN and a password for tack_migrator without granting a
// base role.
type seededRole struct {
	login string
	base  string
}

// seededRoles lists the login roles in the order seed-roles changes them.
var seededRoles = []seededRole{
	{login: "tack_audit_writer", base: "audit_writer"},
	{login: "tack_audit_reader", base: "audit_reader"},
	{login: "tack_audit_redactor", base: "audit_redactor"},
	{login: "tack_audit_operator", base: "audit_operator"},
	{login: "tack_app", base: "app_auth"},
	{login: migration.MigratorRole, base: ""},
}

type roleState struct {
	Exists   bool     `json:"exists"`
	Login    bool     `json:"login"`
	MemberOf []string `json:"member_of"`
}

// roleChangeEvidence identifies the role and its state before the command.
type roleChangeEvidence struct {
	Role     string    `json:"role"`
	BaseRole string    `json:"base_role,omitempty"`
	Before   roleState `json:"before"`
}

func (roleChangeEvidence) isStepEvidence() {}

func seedRolesPasswordsSet(cfg *config.Config) bool {
	return !slices.Contains(
		[]string{
			cfg.AuditWriterPassword,
			cfg.AuditReaderPassword,
			cfg.AuditRedactorPassword,
			cfg.AuditOperatorPassword,
			cfg.AppPassword,
			cfg.MigratorPassword,
		},
		"",
	)
}

func queryRoleStates(ctx context.Context, pool *pgxpool.Pool) (map[string]roleState, error) {
	logins := make([]string, 0, len(seededRoles))
	for _, role := range seededRoles {
		logins = append(logins, role.login)
	}

	rows, err := pool.Query(ctx, seededRoleStateQuery, logins, auditBaseRoles)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"audit.seed_roles.state_read_failed",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("audit seed-roles: read the login roles: %w", err)
	}

	states := make(map[string]roleState, len(seededRoles))
	for _, role := range seededRoles {
		states[role.login] = roleState{Exists: false, Login: false, MemberOf: []string{}}
	}

	defer rows.Close()

	for rows.Next() {
		var name string
		var login bool
		var memberOf []string
		if err := rows.Scan(&name, &login, &memberOf); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.seed_roles.state_read_failed",
				slog.String("err", err.Error()),
			)

			return nil, fmt.Errorf("audit seed-roles: scan a login role: %w", err)
		}

		states[name] = roleState{Exists: true, Login: login, MemberOf: memberOf}
	}

	if err := rows.Err(); err != nil {
		slog.ErrorContext(
			ctx,
			"audit.seed_roles.state_read_failed",
			slog.String("err", err.Error()),
		)

		return nil, fmt.Errorf("audit seed-roles: scan the login roles: %w", err)
	}

	return states, nil
}

// runAuditSeedRolesRecorded saves each role attempt before seeding the roles.
func runAuditSeedRolesRecorded(
	ctx context.Context,
	cfg *config.Config,
	steps audit.StepRecorder,
) (audit.OutcomeDetails, error) {
	if !seedRolesPasswordsSet(cfg) {
		err := RunAuditSeedRoles(ctx, cfg)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}

	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.pool_open_failed", slog.String("err", err.Error()))

		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), fmt.Errorf(
			"audit seed-roles: open admin pool: %w",
			err,
		)
	}
	defer pool.Close()
	before, err := readSeededRoleStates(ctx, pool)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}

	begun := make([]audit.Event, 0, len(seededRoles))
	for _, role := range seededRoles {
		evidence := roleChangeEvidence{
			Role:     role.login,
			BaseRole: role.base,
			Before:   before[role.login],
		}
		encoded, err := encodeEvidence(ctx, evidence)
		if err != nil {
			return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
		}

		step := audit.ExternalStep(externalTarget("yugabyte/role/"+role.login), encoded)
		if err := steps.BeginStep(ctx, step); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.seed_roles.step_record_failed",
				slog.String("err", err.Error()),
			)

			return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), fmt.Errorf(
				"audit seed-roles: record role %s: %w",
				role.login,
				err,
			)
		}

		begun = append(begun, step)
	}

	runErr := RunAuditSeedRoles(ctx, cfg)
	details := commandOutcome(audit.OutcomeCompleted, "", nil)
	if runErr != nil {
		details = commandOutcome(audit.OutcomeFailed, commandFailedCode, runErr)
	}

	for _, step := range begun {
		if err := steps.FinishStep(ctx, audit.FinishedStep(step, details, nil)); err != nil {
			slog.ErrorContext(
				ctx,
				"audit.seed_roles.step_record_failed",
				slog.String("err", err.Error()),
			)

			return details, fmt.Errorf(
				"audit seed-roles: finish role %s: %w",
				step.Context.Target.Reference,
				errors.Join(runErr, err),
			)
		}
	}

	return details, runErr
}
