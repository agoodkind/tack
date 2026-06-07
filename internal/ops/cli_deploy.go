package ops

import (
	"context"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/cli"
)

// deployCommand builds the `ops deploy` subtree. The bare command runs the full
// build, push, pull, up, verify flow; each subcommand stops after one step.
func deployCommand(f *cli.Factory) *cobra.Command {
	step := func(run func(context.Context, *deployContext) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, _ []string) error {
			dctx, err := newDeployContext(cmd.Context(), f.Cfg)
			if err != nil {
				return err
			}
			return run(cmd.Context(), dctx)
		}
	}
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Build, push, and roll the production image to CT 117",
		Long:  "Run the canonical build, push, pull, up, verify flow. Each subcommand stops after one step.",
		RunE:  step(runDeployAll),
	}
	cmd.AddCommand(
		&cobra.Command{Use: "build", Short: "Compile the tack-server image locally", RunE: step(runDeployBuild)},
		&cobra.Command{Use: "push", Short: "Ship the image to the registry or save it for offline load", RunE: step(runDeployPush)},
		&cobra.Command{Use: "pull", Short: "Fetch the image on the remote host", RunE: step(runDeployPull)},
		&cobra.Command{Use: "up", Short: "Roll the running container with docker compose up -d", RunE: step(runDeployUp)},
		&cobra.Command{Use: "verify", Short: "Assert the running digest matches the pushed image", RunE: step(runDeployVerify)},
	)
	return cmd
}
