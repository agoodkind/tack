// YugabyteDB snapshot exports include database schema and cluster roles as
// separate SQL artifacts. Schema dumps retain GRANT and REVOKE statements.
// Restored row-level security policies require those privileges (TACK-474);
// recovery does not rerun the migrations that granted them. Roles require a
// cluster-wide dump because a database schema dump cannot create them.

package ops

import (
	"context"
	"os"
	"strconv"

	"github.com/moby/moby/client"

	"goodkind.io/tack/internal/config"
)

// stageWriterUser returns this process's UID and GID. Dump containers use
// this identity when writing files that the export reads and uploads.
func stageWriterUser() string {
	return strconv.Itoa(os.Getuid()) + ":" + strconv.Itoa(os.Getgid())
}

const (
	// ysqlDumpBinary identifies the single-database dumper in the engine image.
	ysqlDumpBinary = "/home/yugabyte/postgres/bin/ysql_dump"
	// ysqlDumpAllBinary identifies the cluster-wide dumper for roles and memberships.
	ysqlDumpAllBinary = "/home/yugabyte/postgres/bin/ysql_dumpall"
	// ybDumpPort specifies the dumpers' YSQL port.
	ybDumpPort = "5433"
	// ybDumpOutDir specifies the staging bind mount and output directory.
	ybDumpOutDir = "/out"
	// ybDumpPasswordVar specifies the password environment variable separately
	// from its value to avoid false credential-scanner matches.
	ybDumpPasswordVar = "PGPASSWORD"
)

// ybSchemaDumpArgs returns schema dump arguments after shared connection flags.
// The dump includes every GRANT and REVOKE and omits environment-specific
// ownership. FORCE ROW LEVEL SECURITY in migrations/002_audit.sql subjects
// owners to the policies too; ownership does not replace base-role membership.
// The dump omits --dump-role-checks to make missing roles fail restoration
// instead of silently skipping privilege statements.
func ybSchemaDumpArgs(cfg *config.Config) []string {
	return []string{
		"-d",
		cfg.YugabyteDB,
		"--schema-only",
		"--include-yb-metadata",
		"--no-owner",
		"-f",
		ybDumpOutDir + "/" + ybSnapshotSchemaObject,
	}
}

// ybRolesDumpArgs exports role identities and memberships without passwords.
// Restored login roles cannot authenticate with a password until
// `ops audit seed-roles` sets one. The export never stores those credentials.
func ybRolesDumpArgs(cfg *config.Config) []string {
	return []string{
		"-l",
		cfg.YugabyteDB,
		"--roles-only",
		"--no-role-passwords",
		"-f",
		ybDumpOutDir + "/" + ybSnapshotRolesObject,
	}
}

// ybDumpSpec configures one dump container, its host output file, and its
// diagnostic labels and events. attemptEvent records each endpoint refusal,
// including refusals before another endpoint succeeds.
type ybDumpSpec struct {
	binary       string
	args         []string
	outPath      string
	label        string
	okEvent      string
	attemptEvent string
	failEvent    string
}

// ybSchemaDumpSpec configures schema export with YugabyteDB metadata for the
// table IDs and properties that snapshot import requires.
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

// ybRolesDumpSpec configures role export. Schema privilege statements require
// these roles to exist before the restore applies them.
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

// dumpYBSchemaOneShot writes a schema-only dump into the staging bind mount.
func dumpYBSchemaOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	stageDir string,
	schemaPath string,
) error {
	return runYBDumpOneShot(ctx, cli, cfg, stageDir, ybSchemaDumpSpec(cfg, schemaPath))
}

// dumpYBRolesOneShot writes cluster roles into the staging bind mount.
func dumpYBRolesOneShot(
	ctx context.Context,
	cli *client.Client,
	cfg *config.Config,
	stageDir string,
	rolesPath string,
) error {
	return runYBDumpOneShot(ctx, cli, cfg, stageDir, ybRolesDumpSpec(cfg, rolesPath))
}
