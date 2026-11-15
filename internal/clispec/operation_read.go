package clispec

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// runReadOperation records the attempted read before writing command output.
func runReadOperation[I Input](ctx context.Context, f *cli.Factory, op Operation[I], in I) error {
	input, err := json.Marshal(in)
	if err != nil {
		return loggedAuditError(ctx, "encode command input", err)
	}

	spec := op.auditSpec()
	run := op.Run
	if !f.Execute() {
		if err := printReadDryRun(
			withDryRunOutput(ctx, dryRunDestination(f, f.Out)),
			f,
			spec,
			input,
		); err != nil {
			return err
		}

		if op.DryRun == nil {
			return nil
		}

		run = op.DryRun
	}

	recordingFactory := *f
	recordingFactory.SetAuditInfrastructureProbe(nil)
	_, err = RunAuditedRead(
		ctx,
		&recordingFactory,
		spec,
		input,
		func(ctx context.Context) (audit.OperatorReadOutput, error) {
			runErr := run(ctx, in, NewCLISink(f))

			return audit.OperatorReadOutput{}, runErr
		},
	)

	return err
}
