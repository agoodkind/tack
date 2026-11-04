package clispec

import (
	"context"
	"encoding/json"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// runReadOperation finalizes the read event before printing captured results.
func runReadOperation[I Input](ctx context.Context, f *cli.Factory, op Operation[I], in I) error {
	input, err := json.Marshal(in)
	if err != nil {
		return loggedAuditError(ctx, "encode command input", err)
	}
	spec := op.auditSpec()
	run := op.Run
	if !f.Execute() {
		if err := printReadDryRun(withDryRunOutput(ctx, dryRunDestination(f, f.Out)), f, spec, input); err != nil {
			return err
		}
		if op.DryRun == nil {
			return nil
		}
		run = op.DryRun
	}
	captured := &operationReadSink{captureSink: captureSink{writes: nil}, targets: nil}
	// Typed runtime reads require lifecycle admission during infrastructure repairs.
	recordingFactory := *f
	recordingFactory.SetAuditInfrastructureProbe(nil)
	_, err = RunAuditedRead(ctx, &recordingFactory, spec, input, func(ctx context.Context) (audit.OperatorReadOutput, error) {
		if err := run(ctx, in, captured); err != nil {
			return audit.OperatorReadOutput{}, err
		}
		return captured.output(captured.targets), nil
	})
	if err != nil {
		return err
	}
	return captured.replay(ctx, NewCLISink(f))
}

type operationReadSink struct {
	captureSink
	targets []audit.Target
}

func (s *operationReadSink) addReadTargets(targets []audit.Target) {
	s.targets = append(s.targets, targets...)
}
