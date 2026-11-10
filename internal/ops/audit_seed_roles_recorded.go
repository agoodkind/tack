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

// roleChangeUnknownCode is the error code of a role step after a failed seed
// when the role attributes and memberships did not change. The step cannot
// establish whether the seed rotated the password.
const roleChangeUnknownCode = "role_change_unknown"

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

// roleChangeEvidence is the evidence of one role step. It records whether the
// seed set a password and never records the password.
type roleChangeEvidence struct {
	Role        string     `json:"role"`
	BaseRole    string     `json:"base_role,omitempty"`
	Before      roleState  `json:"before"`
	After       *roleState `json:"after,omitempty"`
	PasswordSet bool       `json:"password_set"`
}

func (roleChangeEvidence) isStepEvidence() {}

func seedRolesPasswordsSet(cfg *config.Config) bool {
	return !slices.Contains([]string{
		cfg.AuditWriterPassword, cfg.AuditReaderPassword, cfg.AuditRedactorPassword,
		cfg.AuditOperatorPassword, cfg.AppPassword, cfg.MigratorPassword,
	}, "")
}

func queryRoleStates(ctx context.Context, pool *pgxpool.Pool) (map[string]roleState, error) {
	logins := make([]string, 0, len(seededRoles))
	for _, role := range seededRoles {
		logins = append(logins, role.login)
	}
	rows, err := pool.Query(ctx, seededRoleStateQuery, logins, auditBaseRoles)
	if err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.state_read_failed", slog.String("err", err.Error()))
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
			slog.ErrorContext(ctx, "audit.seed_roles.state_read_failed", slog.String("err", err.Error()))
			return nil, fmt.Errorf("audit seed-roles: scan a login role: %w", err)
		}
		states[name] = roleState{Exists: true, Login: login, MemberOf: memberOf}
	}
	if err := rows.Err(); err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.state_read_failed", slog.String("err", err.Error()))
		return nil, fmt.Errorf("audit seed-roles: scan the login roles: %w", err)
	}
	return states, nil
}

// runAuditSeedRolesRecorded runs seed-roles with one step per login role. It
// records the role attributes and audit base role memberships before and
// after the seed. It refuses before any change when a password is missing.
func runAuditSeedRolesRecorded(ctx context.Context, cfg *config.Config, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
	if !seedRolesPasswordsSet(cfg) {
		err := RunAuditSeedRoles(ctx, cfg)
		return commandOutcome(audit.OutcomeRefused, commandConfigCode, err), err
	}
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, &telemetry.QueryTracer{})
	if err != nil {
		slog.ErrorContext(ctx, "audit.seed_roles.pool_open_failed", slog.String("err", err.Error()))
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), fmt.Errorf("audit seed-roles: open admin pool: %w", err)
	}
	defer pool.Close()
	before, err := readSeededRoleStates(ctx, pool)
	if err != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
	}
	begun := make([]audit.Event, 0, len(seededRoles))
	for _, role := range seededRoles {
		evidence := roleChangeEvidence{Role: role.login, BaseRole: role.base, Before: before[role.login], After: nil, PasswordSet: false}
		encoded, err := encodeEvidence(ctx, evidence)
		if err != nil {
			return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), err
		}
		step := audit.ExternalStep(externalTarget("yugabyte/role/"+role.login), encoded)
		if err := steps.BeginStep(ctx, step); err != nil {
			slog.ErrorContext(ctx, "audit.seed_roles.step_record_failed", slog.String("err", err.Error()))
			return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), fmt.Errorf("audit seed-roles: record role %s: %w", role.login, err)
		}
		begun = append(begun, step)
	}
	runErr := RunAuditSeedRoles(ctx, cfg)
	after, readErr := readSeededRoleStates(ctx, pool)
	if readErr != nil {
		return commandOutcome(audit.OutcomeIncomplete, commandFailedCode, readErr), errors.Join(runErr, readErr)
	}
	for index, role := range seededRoles {
		if err := finishRoleStep(ctx, steps, begun[index], role, before[role.login], after[role.login], runErr); err != nil {
			return commandOutcome(audit.OutcomeFailed, commandFailedCode, err), errors.Join(runErr, err)
		}
	}
	if runErr != nil {
		return commandOutcome(audit.OutcomeFailed, commandFailedCode, runErr), runErr
	}
	return commandOutcome(audit.OutcomeCompleted, "", nil), nil
}
