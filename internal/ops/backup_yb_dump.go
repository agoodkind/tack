// The snapshot export writes SQL text for the database schema and cluster
// roles. Together these artifacts recreate database structure and access
// control. The schema dump includes grants and revokes from backup time.
//
// The earlier dump omitted privileges. A restored ledger had tables, rows,
// and row-level security policies but lacked the privileges those policies
// required, preventing application roles from reading it (TACK-474).
// Recovery does not rerun the original grant migration on a restored
// database. The backup must preserve those privileges.
//
// Roles apply to the whole cluster instead of one database. A separate roles
// dump supplies them because the schema dump does not include them.

package ops

import (
	"context"
	"os"
	"strconv"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
)

// stageWriterUser returns this process's user and group IDs. Containers
// that write staging files run with these IDs. The export reads and uploads
// the resulting files.
func stageWriterUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}

const (
	// ysqlDumpBinary is the single-database dumper inside the engine image.
	ysqlDumpBinary = "/home/yugabyte/postgres/bin/ysql_dump"
	// ysqlDumpAllBinary selects the cluster-wide dumper that exports roles and
	// role memberships.
	ysqlDumpAllBinary = "/home/yugabyte/postgres/bin/ysql_dumpall"
	// ybDumpPort specifies the YSQL port for both dumpers.
	ybDumpPort = "5433"
	// ybDumpOutDir specifies the staging directory's mount path in the temporary
	// container. Both dumpers write their output there.
	ybDumpOutDir = "/out"
	// ybDumpPasswordVar specifies the environment variable that supplies the
	// dumpers' password. A separate constant avoids placing a variable name
	// beside its value where a secret scanner could mistake it for a credential.
	ybDumpPasswordVar = "PGPASSWORD"
)

// ybSchemaDumpArgs returns schema-dump arguments after the shared connection
// flags. It omits --no-privileges to preserve database GRANT and REVOKE
// statements.
//
// The dump uses --no-owner because ownership depends on the restore
// environment. Audit tables use FORCE ROW LEVEL SECURITY. Owners subject to
// forced row-level security require an applicable SELECT policy to read rows;
// ownership alone does not grant that access. A policy can authorize a role
// directly or through base-role membership.
//
// The dump omits --dump-role-checks. That flag would make each privilege
// statement conditional on the role's existence and permit a restore to
// skip required grants when a role is missing.
func ybSchemaDumpArgs(cfg *config.Config) []string {
	return []string{
		"-d", cfg.YugabyteDB,
		"--schema-only",
		"--include-yb-metadata",
		"--no-owner",
		"-f", ybDumpOutDir + "/" + ybSnapshotSchemaObject,
	}
}

// ybRolesDumpArgs returns role and membership dump arguments after the
// shared connection flags. The dump uses --no-role-passwords to export role
// identities without credentials. A restored login role cannot authenticate
// with a password until ops audit seed-roles assigns one.
func ybRolesDumpArgs(cfg *config.Config) []string {
	return []string{
		"-l", cfg.YugabyteDB,
		"--roles-only",
		"--no-role-passwords",
		"-f", ybDumpOutDir + "/" + ybSnapshotRolesObject,
	}
}

// ybDumpSpec supplies a dumper binary, its arguments after the shared
// connection flags, the host output path, and its log events and error label.
// attemptEvent records an endpoint refusal even if a later endpoint succeeds.
// One refusal does not mean the whole dump failed.
type ybDumpSpec struct {
	binary       string
	args         []string
	outPath      string
	label        string
	okEvent      string
	attemptEvent string
	failEvent    string
}

// ybSchemaDumpSpec configures the schema dump. export_snapshot metadata
// references the table IDs that schema import recreates. The dump uses
// --include-yb-metadata to preserve YugabyteDB table properties for import.
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

// ybRolesDumpSpec configures the roles dump. Schema privilege statements
// require existing roles and do not create them. The roles artifact supplies
// those identities during restore.
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

// dumpYBSchemaOneShot writes a schema-only database dump into the mounted
// staging directory.
func dumpYBSchemaOneShot(ctx context.Context, cli *client.Client, cfg *config.Config, stageDir, schemaPath string) error {
	return runYBDumpOneShot(ctx, cli, cfg, stageDir, ybSchemaDumpSpec(cfg, schemaPath))
}

// dumpYBRolesOneShot writes cluster roles into the mounted staging directory.
func dumpYBRolesOneShot(ctx context.Context, cli *client.Client, cfg *config.Config, stageDir, rolesPath string) error {
	return runYBDumpOneShot(ctx, cli, cfg, stageDir, ybRolesDumpSpec(cfg, rolesPath))
}
