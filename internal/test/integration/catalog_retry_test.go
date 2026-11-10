package integration_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

func TestRuntimeAuditSeedRolesCatalogRetry(t *testing.T) {
	node := testenv.StartEmptyLedger(t, "tack-test-catalog-roles-"+uuid.NewString()[:8])
	err := migration.Migrate(t.Context(), node.DSN, migrations.FS)
	if err != nil {
		t.Fatalf("migrate the disposable ledger: %v", err)
	}

	admin, err := pgxpool.New(t.Context(), node.DSN)
	if err != nil {
		t.Fatal("open the disposable ledger")
	}

	t.Cleanup(admin.Close)
	commandPool, err := pgxpool.New(t.Context(), node.DSN)
	if err != nil {
		t.Fatal("open the command pool")
	}

	t.Cleanup(commandPool.Close)

	t.Setenv("DATABASE_URL", node.DSN)
	t.Setenv("AUDIT_OPERATOR_DSN", node.DSN)
	t.Setenv("FDB_CLUSTER_FILE", testenv.FoundationDB(t))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("load the disposable configuration")
	}

	passwords := []*string{
		&cfg.AuditWriterPassword,
		&cfg.AuditReaderPassword,
		&cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword,
		&cfg.AppPassword,
		&cfg.MigratorPassword,
	}
	var secrets []string

	for _, password := range passwords {
		*password = syntheticRolePassword(t)
		secrets = append(secrets, *password)
	}

	actor := uuid.Must(uuid.NewV7())
	statement := fmt.Sprintf(
		readTestSQL(t, "runtime_audit_seed_roles_catalog_retry.sql"),
		actor.String(),
	)
	_, err = admin.Exec(t.Context(), statement)
	if err != nil {
		t.Fatalf("install the catalog rejection fixture: %v", err)
	}

	output, err := runOpsCommand(
		t,
		cfg,
		commandPool,
		"--execute",
		"--operator-id",
		actor.String(),
		"--operator-email",
		outcomeOperatorEmail,
		"ops",
		"audit",
		"seed-roles",
	)
	if err != nil {
		t.Fatalf("seed-roles did not recover from the database rejection: %v", err)
	}

	assertRoleSecretsAbsent(t, []byte(output), secrets...)

	var sequence int64
	err = admin.QueryRow(
		t.Context(),
		readTestSQL(t, "runtime_audit_seed_roles_catalog_sequence.sql"),
	).Scan(
		&sequence,
	)
	if err != nil || sequence <= 2 {
		t.Fatalf(
			"catalog rejection sequence = %d, error %v; the command must retry the refused inserts",
			sequence,
			err,
		)
	}

	events := outboxEventsByQuery(
		t,
		admin,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbAuditRolesSeed),
		actor.String(),
	)
	assertSeedRolesOutcome(t, events, audit.OutcomeCompleted)
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	results := eventsInPhase(events, audit.PhaseResult, intents[0].Context.OperationID)
	requireRoleSteps(t, results)

	for _, event := range events {
		if event.Actor.ID != actor {
			t.Fatalf("event %s changed the initiating actor", event.EventID)
		}

		encoded, encodeErr := json.Marshal(event)
		if encodeErr != nil {
			t.Fatalf("encode event %s: %v", event.EventID, encodeErr)
		}

		assertRoleSecretsAbsent(t, encoded, secrets...)
	}

	verifySeedRolesPermanentLedger(t, node.DSN, admin, events, secrets)
}
