// backup_yb_dump.go writes the SQL-text artifacts of a YugabyteDB snapshot
// export: the database's schema and the cluster's roles. Between them they
// describe what the restored database holds and who may read it.
//
// The schema dump carries the grants and revokes exactly as they stood at
// backup time. It used to exclude them, which left a restored ledger with its
// tables, its rows, and its row-level-security policies, but with none of the
// privileges those policies depend on, so no application role could read it
// (TACK-474). The privileges are not re-creatable from anywhere else in a
// recovery: the migration that first granted them is never re-run against a
// restored database.
//
// Roles are cluster objects rather than database objects, so the schema dump
// cannot carry them and the roles dump is a separate artifact.

package ops

import (
	"context"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
)

const (
	// ysqlDumpBinary is the single-database dumper inside the engine image.
	ysqlDumpBinary = "/home/yugabyte/postgres/bin/ysql_dump"
	// ysqlDumpAllBinary is the cluster-wide dumper, the only one that
	// describes roles and their memberships.
	ysqlDumpAllBinary = "/home/yugabyte/postgres/bin/ysql_dumpall"
	// ybDumpPort is the YSQL port both dumpers connect on.
	ybDumpPort = "5433"
	// ybDumpOutDir is where the staging dir is bind-mounted inside the
	// one-shot, which is where both dumpers write their file.
	ybDumpOutDir = "/out"
)

// ybSchemaDumpArgs is the argument vector, after the shared connection flags,
// that describes the database's schema.
//
// No --no-privileges: the artifact must describe its own access control, so
// the dump carries every GRANT and REVOKE the database holds. --no-owner
// stays, because ownership is a property of the environment rather than of the
// data, and it grants no read the restore needs: every audit table is declared
// FORCE ROW LEVEL SECURITY in migrations/002_audit.sql, which subjects the
// owner to the same policies as anyone else, so an owner who is not a member
// of a base role sees nothing regardless.
//
// --dump-role-checks is deliberately absent. It would make each privilege
// statement conditional on its role existing, which turns a missing role from
// a failed restore into a restore that quietly grants nothing: the same silent
// success this artifact exists to remove.
func ybSchemaDumpArgs(cfg *config.Config) []string {
	return []string{
		"-d", cfg.YugabyteDB,
		"--schema-only",
		"--include-yb-metadata",
		"--no-owner",
		"-f", ybDumpOutDir + "/" + ybSnapshotSchemaObject,
	}
}

// ybRolesDumpArgs is the argument vector, after the shared connection flags,
// that describes the cluster's roles and their memberships.
//
// --no-role-passwords is deliberate and load-bearing: the artifact gains the
// identities the ledger's policies name, never the credentials behind them, so
// restoring access control never turns the export into a place passwords live.
// A restored login role therefore has no password until `ops audit seed-roles`
// sets one, which is where those passwords are owned.
func ybRolesDumpArgs(cfg *config.Config) []string {
	return []string{
		"-l", cfg.YugabyteDB,
		"--roles-only",
		"--no-role-passwords",
		"-f", ybDumpOutDir + "/" + ybSnapshotRolesObject,
	}
}

// ybDumpSpec is one dump one-shot: which dumper runs, the arguments after the
// shared connection flags, where the file it writes lands on the host, and the
// log events and error text that name it. attemptEvent names one endpoint's
// refusal, which is a note about the cluster rather than a failed dump, and is
// logged even on a run that goes on to succeed elsewhere.
type ybDumpSpec struct {
	binary       string
	args         []string
	outPath      string
	label        string
	okEvent      string
	attemptEvent string
	failEvent    string
}

// ybSchemaDumpSpec describes the schema dump's one-shot. The export_snapshot
// metadata references table ids this schema recreates on import;
// --include-yb-metadata preserves the YugabyteDB table properties the import
// path needs.
func ybSchemaDumpSpec(cfg *config.Config, schemaPath string) ybDumpSpec {
	return ybDumpSpec{
		binary:       ysqlDumpBinary,
		args:         ybSchemaDumpArgs(cfg),
		outPath:      schemaPath,
		label:        "schema",
		okEvent:      "backup.yb_snapshot.schema_dumped",
		attemptEvent: "backup.yb_snapshot.schema_endpoint_refused",
		failEvent:    "backup.yb_snapshot.schema_failed",
	}
}

// ybRolesDumpSpec describes the roles dump's one-shot. Without the artifact it
// writes, a restore has no identity to grant anything to: the schema's
// privilege statements name roles, and the restore that applies them creates
// none.
func ybRolesDumpSpec(cfg *config.Config, rolesPath string) ybDumpSpec {
	return ybDumpSpec{
		binary:       ysqlDumpAllBinary,
		args:         ybRolesDumpArgs(cfg),
		outPath:      rolesPath,
		label:        "roles",
		okEvent:      "backup.yb_snapshot.roles_dumped",
		attemptEvent: "backup.yb_snapshot.roles_endpoint_refused",
		failEvent:    "backup.yb_snapshot.roles_failed",
	}
}

// dumpYBSchemaOneShot writes a schema-only dump of the database into the
// bind-mounted stage dir.
func dumpYBSchemaOneShot(ctx context.Context, cli *client.Client, cfg *config.Config, stageDir, schemaPath string) error {
	return runYBDumpOneShot(ctx, cli, cfg, stageDir, ybSchemaDumpSpec(cfg, schemaPath))
}

// dumpYBRolesOneShot writes the cluster's roles into the bind-mounted stage
// dir.
func dumpYBRolesOneShot(ctx context.Context, cli *client.Client, cfg *config.Config, stageDir, rolesPath string) error {
	return runYBDumpOneShot(ctx, cli, cfg, stageDir, ybRolesDumpSpec(cfg, rolesPath))
}
