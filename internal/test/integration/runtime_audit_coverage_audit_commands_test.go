//go:build runtimeaudit

package integration_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

// addAuditCommandCases adds audit command cases with their expected audit verbs
// and outcomes.
func addAuditCommandCases(cases map[string]coverageCase) {
	reader := func(t *testing.T, env *coverageEnv) func(*config.Config) {
		ledger, brokers, topic := testenv.Ledger(t), testenv.Kafka(t), env.ledger.stack.topic
		return func(cfg *config.Config) {
			cfg.AuditReaderDSN, cfg.AuditKafkaBrokers, cfg.AuditKafkaTopic = ledger, brokers, topic
		}
	}
	cases["command ops audit consumer-offsets"] = commandCase(audit.VerbOpsAuditConsumerOffsets, audit.OutcomeOK,
		func(t *testing.T, env *coverageEnv) commandInput {
			return commandInput{args: []string{"ops", "audit", "consumer-offsets"}, configure: reader(t, env), outbox: nil}
		})
	cases["command ops audit dlq inspect"] = commandCase(audit.VerbOpsAuditDLQInspect, audit.OutcomeOK,
		func(t *testing.T, env *coverageEnv) commandInput {
			return commandInput{args: []string{"ops", "audit", "dlq", "inspect"}, configure: reader(t, env), outbox: nil}
		})
	cases["command ops audit dlq replay"] = commandCase(audit.VerbOpsAuditDLQReplay, audit.OutcomeOK,
		func(t *testing.T, env *coverageEnv) commandInput {
			return commandInput{args: []string{"ops", "audit", "dlq", "replay", "--limit", "1"}, configure: reader(t, env), outbox: nil}
		})
	cases["command ops audit prove-schema-guard"] = commandCase(audit.VerbOpsAuditSchemaGuardProof, audit.OutcomeOK,
		fixedCommand("ops", "audit", "prove-schema-guard"))
	cases["command ops audit seed-roles"] = commandCase(audit.VerbAuditRolesSeed, audit.OutcomeCompleted, seedRolesInput)
}

// seedRolesInput prepares a migrated YugabyteDB node and passwords for the
// seed-roles command. The case checks that node's operator outbox.
func seedRolesInput(t *testing.T, _ *coverageEnv) commandInput {
	t.Helper()
	node := testenv.StartEmptyLedger(t, "tack-test-coverage-roles-"+uuid.NewString()[:8])
	if err := migration.Migrate(t.Context(), node.DSN, migrations.FS); err != nil {
		t.Fatalf("Role-seeding setup failed to apply migrations; the test requires a migrated database: %v", err)
	}
	pool, err := pgxpool.New(t.Context(), node.DSN)
	if err != nil {
		t.Fatalf("Role-seeding setup failed to create a connection pool; the test requires access to the operator outbox: %v", err)
	}
	t.Cleanup(pool.Close)
	return commandInput{
		args: []string{"ops", "audit", "seed-roles"}, outbox: pool,
		configure: func(cfg *config.Config) {
			cfg.DatabaseURL = node.DSN
			for _, password := range []*string{
				&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
				&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
			} {
				*password = "coverage-role-secret-" + uuid.NewString()
			}
		},
	}
}

// addAuthCommandCases adds token creation, listing, and revocation cases with
// their expected audit verbs and outcomes.
func addAuthCommandCases(cases map[string]coverageCase) {
	cases["command ops auth token-create"] = commandCase(audit.VerbOpsAuthTokenCreate, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: []string{"ops", "auth", "token-create", "--email", env.userEmail(t), "--label", "coverage"}, configure: nil, outbox: nil}
	})
	cases["command ops auth token-list"] = commandCase(audit.VerbOpsAuthTokenList, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		return commandInput{args: []string{"ops", "auth", "token-list", "--email", env.userEmail(t)}, configure: nil, outbox: nil}
	})
	cases["command ops auth token-revoke"] = commandCase(audit.VerbOpsAuthTokenRevoke, audit.OutcomeOK, func(t *testing.T, env *coverageEnv) commandInput {
		if _, output, err := env.command(t, nil, "ops", "auth", "token-create", "--email", env.userEmail(t), "--label", "coverage-revoke"); err != nil {
			t.Fatalf("Token creation failed; the revocation case requires an existing token: %v\n%s", err, output)
		}
		tokens, err := postgres.NewTokenRepo(env.admin).List(t.Context(), env.server.userID)
		if err != nil || len(tokens) == 0 {
			t.Fatalf("Token lookup failed or returned an empty list; the test requires at least one token for user %s: %d %v", env.server.userID, len(tokens), err)
		}
		return commandInput{args: []string{"ops", "auth", "token-revoke", "--id", tokens[0].ID.String()}, configure: nil, outbox: nil}
	})
}

// userEmail reads the current test user's email address from YugabyteDB.
func (env *coverageEnv) userEmail(t *testing.T) string {
	t.Helper()
	current, err := postgres.NewUserRepo(env.admin).GetByID(t.Context(), env.server.userID)
	if err != nil {
		t.Fatalf("User lookup failed; the command case requires the email address for user %s: %v", env.server.userID, err)
	}
	return current.Email
}
