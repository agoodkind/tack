package ops

import (
	"context"
	"encoding/json"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// provisionInput contains the flags of ops provision.
type provisionInput struct {
	AllowFDBInit bool `json:"allow_fdb_init"`
}

func (provisionInput) isCommandInput() {}

// provisionSpec returns the external definition of ops provision. The
// command creates the audit infrastructure on a fresh environment.
func provisionSpec() audit.Spec {
	spec := externalSpec(audit.VerbOpsProvision, "tack/environment")
	spec.CreatesAuditInfrastructure = true
	return spec
}

// provisionCommand builds `ops provision`. The destructive fdb step requires
// --allow-fdb-init (default false), and the command refuses it on a
// configured cluster. With --execute, the command records each step as an
// external step.
func provisionCommand(f *cli.Factory) *cobra.Command {
	in := provisionInput{AllowFDBInit: false}
	cmd := &cobra.Command{
		Use:   "provision",
		Short: "Bring a fresh environment to ready: fdb init (opt-in), migrate, seed audit roles, start fdb continuous backup, seed product",
		Long: "Ordered and idempotent: configure fdb only when unconfigured and --allow-fdb-init is set (fresh environments only, never prod), " +
			"run migrations, seed the audit roles, start the FoundationDB continuous backup session when TACK_BACKUP_FDB_CONTINUOUS is true, " +
			"then seed initial product data when SEED_EMAIL and SEED_NAME are set. Each step does nothing when its work is already done.",
		Args: cobra.NoArgs,
	}
	cmd.Flags().BoolVar(&in.AllowFDBInit, "allow-fdb-init", false,
		"Permit 'fdbcli configure new' when fdb is unconfigured. FRESH ENVIRONMENTS ONLY; never pass on prod.")
	input := func() (json.RawMessage, error) { return encodeCommandInput(in) }
	clispec.AttachExternal(cmd, f, provisionSpec(), input, func(ctx context.Context, steps audit.StepRecorder) (audit.OutcomeDetails, error) {
		return runProvisionRecorded(ctx, f.Cfg, in.AllowFDBInit, clispec.NewCLISink(f), steps)
	})
	return cmd
}
