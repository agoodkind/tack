package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"goodkind.io/tack/internal/adapters/postgres/migration"
	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/ops"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

func TestRuntimeAuditSeedRolesAuthenticationRecovery(t *testing.T) {
	node := testenv.StartEmptyLedger(t, "tack-test-auth-roles-"+uuid.NewString()[:8])
	if err := migration.Migrate(t.Context(), node.DSN, migrations.FS); err != nil {
		t.Fatalf("migrate the disposable ledger: %v", err)
	}

	admin, err := pgxpool.New(t.Context(), node.DSN)
	if err != nil {
		t.Fatalf("open the disposable ledger")
	}

	t.Cleanup(admin.Close)
	oldPassword := syntheticRolePassword(t)

	proposed := make([]string, 6)
	for index := range proposed {
		proposed[index] = syntheticRolePassword(t)
	}

	initial := &config.Config{
		DatabaseURL:           node.DSN,
		AuditWriterPassword:   syntheticRolePassword(t),
		AuditReaderPassword:   syntheticRolePassword(t),
		AuditRedactorPassword: syntheticRolePassword(t),
		AuditOperatorPassword: oldPassword,
		AppPassword:           syntheticRolePassword(t),
		MigratorPassword:      syntheticRolePassword(t),
	}
	if err := ops.RunAuditSeedRoles(t.Context(), initial); err != nil {
		t.Fatalf("prepare the disposable login roles")
	}

	operatorDSN := changedDSN(
		t,
		node.DSN,
		func(parsed *url.URL) {
			parsed.User = url.UserPassword("tack_audit_operator", oldPassword)
		},
	)
	writerDSN := changedDSN(
		t,
		node.DSN,
		func(parsed *url.URL) {
			parsed.User = url.UserPassword("tack_audit_writer", initial.AuditWriterPassword)
		},
	)
	fixtureSQL := readTestSQL(t, "runtime_audit_seed_roles_app_superuser.sql")
	if _, err := admin.Exec(t.Context(), fmt.Sprintf(fixtureSQL, "SUPERUSER")); err != nil {
		t.Fatalf("set the disposable application role fixture: %v", err)
	}

	t.Cleanup(func() {
		admin.Reset()
		if _, err := admin.Exec(
			context.Background(),
			fmt.Sprintf(fixtureSQL, "NOSUPERUSER"),
		); err != nil {
			t.Errorf("restore the disposable application role fixture: %v", err)
		}
	})
	fixture := seedRolesProcessFixture(t, node.DSN, operatorDSN, proposed)
	fixture.secrets = append(
		fixture.secrets,
		oldPassword,
		initial.AuditWriterPassword,
		initial.AuditReaderPassword,
		initial.AuditRedactorPassword,
		initial.AppPassword,
		initial.MigratorPassword,
		writerDSN,
	)
	binary := buildMigrateServer(t)
	actor := uuid.Must(uuid.NewV7())
	checkSeedRolesDryRun(t, binary, fixture, actor, admin)
	observeIntent := func() {
		deadline := time.Now().Add(30 * time.Second)

		for {
			events := outboxEventsByQuery(
				t,
				admin,
				"runtime_audit_verb_outbox.sql",
				string(audit.VerbAuditRolesSeed),
				actor.String(),
			)
			if len(eventsInPhase(events, audit.PhaseIntent, uuid.Nil)) == 1 {
				break
			}

			if time.Now().After(deadline) {
				t.Fatal("the command did not save its intent before the deadline")
			}

			time.Sleep(10 * time.Millisecond)
		}

		assertOperatorAuthentication(t, writerDSN, initial.AuditWriterPassword, true)
		assertOperatorAuthentication(t, writerDSN, proposed[0], false)
	}
	if err := runSeedRolesProcess(t, binary, fixture, actor, nil, observeIntent); err == nil {
		t.Fatal("seed-roles succeeded with the invalid application role")
	}

	assertOperatorAuthentication(t, operatorDSN, proposed[3], true)
	assertOperatorAuthentication(t, operatorDSN, oldPassword, false)
	failed := outboxEventsByQuery(
		t,
		admin,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbAuditRolesSeed),
		actor.String(),
	)
	assertSeedRolesOutcome(t, failed, audit.OutcomeFailed)

	wrong := syntheticRolePassword(t)
	fixture.secrets = append(fixture.secrets, wrong)
	writeProtectedRoleFile(t, fixture.passwordFiles[3], wrong)
	if err := runSeedRolesProcess(t, binary, fixture, actor, nil, nil); err == nil {
		t.Fatal("seed-roles accepted two invalid operator passwords")
	}

	writeProtectedRoleFile(t, fixture.passwordFiles[3], proposed[3])

	for _, change := range []func() []string{
		func() []string {
			return []string{
				"AUDIT_OPERATOR_PASSWORD_FILE=" + filepath.Join(fixture.directory, "missing"),
			}
		},
		func() []string { return []string{"AUDIT_OPERATOR_PASSWORD_FILE=" + fixture.emptyFile} },
		func() []string { return []string{"AUDIT_OPERATOR_PASSWORD=" + wrong} },
		func() []string {
			return []string{
				"AUDIT_OPERATOR_DSN_FILE=" + filepath.Join(fixture.directory, "missing"),
			}
		},
		func() []string { return []string{"AUDIT_OPERATOR_DSN_FILE=" + fixture.emptyFile} },
		func() []string { return []string{"AUDIT_OPERATOR_DSN=" + wrong} },
	} {
		if err := runSeedRolesProcess(t, binary, fixture, actor, change(), nil); err == nil {
			t.Fatal("seed-roles accepted an invalid password file configuration")
		}
	}

	withoutPassword := changedDSN(
		t,
		operatorDSN,
		func(parsed *url.URL) {
			parsed.User = url.User("tack_audit_operator")
		},
	)
	operatorConfig, err := pgx.ParseConfig(operatorDSN)
	if err != nil {
		t.Fatal("parse the disposable operator connection")
	}

	keywordDSN := fmt.Sprintf(
		"host=%s port=%d user=%s dbname=%s sslmode=disable",
		operatorConfig.Host,
		operatorConfig.Port,
		operatorConfig.User,
		operatorConfig.Database,
	)
	bracketHostDSN := fmt.Sprintf(
		"postgres://tack_audit_operator:%s@unused[/%s?host=%s&port=%d&sslmode=disable&pass%%77ord=",
		oldPassword,
		operatorConfig.Database,
		operatorConfig.Host,
		operatorConfig.Port,
	)
	passfile := filepath.Join(fixture.directory, "passfile")
	writeProtectedRoleFile(t, passfile, "*:*:*:tack_audit_operator:"+proposed[3]+"\n")

	for _, dsn := range []string{
		withoutPassword,
		withoutPassword + "&password=",
		keywordDSN,
		keywordDSN + " password=''",
		bracketHostDSN,
	} {
		path := filepath.Join(fixture.directory, "undeclared-operator")
		writeProtectedRoleFile(t, path, dsn)

		for _, ambient := range []string{"PGPASSWORD=" + proposed[3], "PGPASSFILE=" + passfile} {
			logPath := filepath.Join(fixture.directory, "process.log")
			before, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatalf("read the process log: %v", readErr)
			}

			if err := runSeedRolesProcess(
				t,
				binary,
				fixture,
				actor,
				[]string{"AUDIT_OPERATOR_DSN_FILE=" + path, ambient},
				nil,
			); err == nil {
				t.Fatal("seed-roles accepted an undeclared ambient operator password")
			}

			after, readErr := os.ReadFile(logPath)
			if readErr != nil {
				t.Fatalf("read the process log: %v", readErr)
			}

			message := []byte("AUDIT_OPERATOR_DSN must include the current password")
			if bytes.Count(after, message) != bytes.Count(before, message)+1 {
				t.Fatal("seed-roles did not report the missing declared password")
			}
		}
	}

	afterRefusal := outboxEventsByQuery(
		t,
		admin,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbAuditRolesSeed),
		actor.String(),
	)
	if len(afterRefusal) != len(failed) {
		t.Fatalf("refused commands recorded %d additional events", len(afterRefusal)-len(failed))
	}

	admin.Reset()
	if _, err := admin.Exec(t.Context(), fmt.Sprintf(fixtureSQL, "NOSUPERUSER")); err != nil {
		t.Fatalf("repair the disposable application role fixture: %v", err)
	}

	writeProtectedRoleFile(
		t,
		filepath.Join(fixture.directory, "operator"),
		bracketHostDSN+oldPassword,
	)

	for index := range 2 {
		if index == 1 {
			writeProtectedRoleFile(
				t,
				filepath.Join(fixture.directory, "operator"),
				keywordDSN+" password='"+oldPassword+"'",
			)
		}

		if err := runSeedRolesProcess(t, binary, fixture, actor, nil, nil); err != nil {
			t.Fatal("seed-roles failed after repairing the fixture")
		}
	}

	assertOperatorAuthentication(t, operatorDSN, proposed[3], true)
	assertOperatorAuthentication(t, operatorDSN, oldPassword, false)
	events := outboxEventsByQuery(
		t,
		admin,
		"runtime_audit_verb_outbox.sql",
		string(audit.VerbAuditRolesSeed),
		actor.String(),
	)
	intents := eventsInPhase(events, audit.PhaseIntent, uuid.Nil)
	if len(intents) != 3 {
		t.Fatalf("seed-roles recorded %d command intents; want 3", len(intents))
	}

	for _, intent := range intents[1:] {
		assertSeedRolesOutcome(
			t,
			eventsOfOperation(events, intent.Context.OperationID),
			audit.OutcomeCompleted,
		)
	}

	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("encode the audit event: %v", err)
		}

		assertRoleSecretsAbsent(t, encoded, append(proposed, oldPassword, wrong)...)
	}

	verifySeedRolesPermanentLedger(t, node.DSN, admin, events, fixture.secrets)
}
