package ops

import (
	"context"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
	"goodkind.io/tack/internal/clispec"
)

// deployCommand builds the `ops deploy` subtree. The bare command runs the full
// build, push, pull, up, verify flow; each subcommand stops after one step.
func deployCommand(f *cli.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Build, push, and roll the production image to CT 117",
		Long:  "Run the canonical build, push, pull, up, verify flow. Each subcommand stops after one step.",
	}
	attachDeployAudit(cmd, f, audit.VerbOpsDeploy, true, runDeployAll)
	cmd.AddCommand(
		deployLeaf(f, "build", "Compile the tack-server image locally", audit.VerbOpsDeployBuild, true, runDeployBuild),
		deployLeaf(f, "push", "Ship the image to the registry or save it for offline load", audit.VerbOpsDeployPush, true, runDeployPush),
		deployLeaf(f, "pull", "Fetch the image on the remote host", audit.VerbOpsDeployPull, true, runDeployPull),
		deployLeaf(f, "up", "Roll the running container with docker compose up -d", audit.VerbOpsDeployUp, true, runDeployUp),
		deployLeaf(f, "verify", "Assert the running digest matches the pushed image", audit.VerbOpsDeployVerify, false, runDeployVerify),
	)
	return cmd
}

func deployLeaf(
	f *cli.Factory,
	use string,
	short string,
	verb audit.Verb,
	mutates bool,
	run func(context.Context, *deployContext) error,
) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short}
	attachDeployAudit(cmd, f, verb, mutates, run)
	return cmd
}

func attachDeployAudit(
	cmd *cobra.Command,
	f *cli.Factory,
	verb audit.Verb,
	mutates bool,
	run func(context.Context, *deployContext) error,
) {
	spec := audit.Spec{Verb: string(verb), Mutates: mutates, Reads: !mutates}
	clispec.AttachAudit(cmd, f, spec, func(ctx context.Context) error {
		deployContext, err := newDeployContext(ctx, f.Cfg)
		if err != nil {
			return err
		}
		return run(ctx, deployContext)
	})
}
