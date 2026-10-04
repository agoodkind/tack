package ops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/adapters/postgres"
	"goodkind.io/tack/internal/config"
	"goodkind.io/tack/internal/testenv"
	"goodkind.io/tack/migrations"
)

// TestYBDumpsRunAsMigrator migrates an empty ledger as the engine superuser,
// as a first boot does, and runs both dump one-shots as tack_migrator
// (TACK-554).
func TestYBDumpsRunAsMigrator(t *testing.T) {
	ctx, cli := scratchDrillDocker(t)
	node := testenv.StartEmptyLedger(t, "tack-test-dump-"+uuid.NewString()[:8])
	if err := postgres.Migrate(ctx, node.DSN, migrations.FS); err != nil {
		t.Fatalf("migrate the empty ledger: %v", err)
	}
	cfg := &config.Config{
		DatabaseURL: node.DSN, YugabyteDB: "tack",
		BackupYBImage:           composeServiceImage(t, "yugabyte"),
		BackupFDBNetwork:        node.Network,
		BackupYBMasterAddresses: node.MasterAddress,
	}
	for _, generated := range []*string{
		&cfg.AuditWriterPassword, &cfg.AuditReaderPassword, &cfg.AuditRedactorPassword,
		&cfg.AuditOperatorPassword, &cfg.AppPassword, &cfg.MigratorPassword,
	} {
		*generated = uuid.NewString()
	}
	if err := RunAuditSeedRoles(ctx, cfg); err != nil {
		t.Fatalf("seed-roles: %v", err)
	}

	stageDir := filepath.Join(testenv.SharedDir(t), "dump")
	if err := os.MkdirAll(stageDir, 0o777); err != nil {
		t.Fatalf("mkdir stage: %v", err)
	}
	// The dumper's user in the engine image differs from this process's user.
	if err := os.Chmod(stageDir, 0o777); err != nil {
		t.Fatalf("chmod stage: %v", err)
	}
	schemaPath := filepath.Join(stageDir, ybSnapshotSchemaObject)
	rolesPath := filepath.Join(stageDir, ybSnapshotRolesObject)
	if err := dumpYBSchemaOneShot(ctx, cli, cfg, stageDir, schemaPath); err != nil {
		t.Fatalf("schema dump as tack_migrator: %v", err)
	}
	if err := dumpYBRolesOneShot(ctx, cli, cfg, stageDir, rolesPath); err != nil {
		t.Fatalf("roles dump as tack_migrator: %v", err)
	}

	schema := readDump(t, schemaPath)
	for _, statement := range []string{
		"CREATE TABLE audit.events", "CREATE EVENT TRIGGER tack_audit_schema_guard",
		"CREATE POLICY events_migrator_select",
	} {
		if !strings.Contains(schema, statement) {
			t.Fatalf("the schema dump lacks %q", statement)
		}
	}
	roles := readDump(t, rolesPath)
	if !strings.Contains(roles, "tack_migrator") || !strings.Contains(roles, "tack_audit_writer") {
		t.Fatal("the roles dump lacks tack_migrator or tack_audit_writer")
	}
	if strings.Contains(roles, "PASSWORD") {
		t.Fatal("the roles dump includes a role password")
	}
}

func readDump(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}
