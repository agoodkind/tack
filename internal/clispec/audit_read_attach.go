package clispec

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/spf13/cobra"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

// AttachRead sets audit metadata and RunE on a handwritten Cobra command that
// reads state. When the operator passes --execute, RunE encodes input, calls
// RunAuditedRead with read, and writes the returned payload as text. Without
// --execute, RunE validates the target and operator and prints the dry-run
// preamble without calling read.
func AttachRead(
	cmd *cobra.Command,
	f *cli.Factory,
	spec audit.Spec,
	input func() (json.RawMessage, error),
	read OperatorRead,
) {
	rendered := func(ctx context.Context) (audit.OperatorReadOutput, error) {
		output, readErr := read(ctx)
		if readErr != nil && !audit.IsReadVerdict(readErr) {
			return output, readErr
		}

		captured := &captureSink{writes: nil}
		if err := captured.WriteText(ctx, string(output.Payload)); err != nil {
			return audit.OperatorReadOutput{}, err
		}

		textOutput, renderErr := captured.rendered(ctx, f, output.Targets)
		if renderErr != nil {
			return audit.OperatorReadOutput{}, renderErr
		}

		return textOutput, readErr
	}
	attachRead(
		cmd,
		f,
		spec,
		input,
		rendered,
		func(ctx context.Context, payload []byte) error {
			return writeRendered(ctx, f, payload)
		},
	)
}

// AttachRawRead works like AttachRead. After the final read event commits, it
// writes the returned payload bytes unchanged to the command output without the
// trace header or JSON envelope.
//
// Commands that supply their own output format use this method to preserve it.
//
// Example:
//
//	AttachRawRead(cmd, f, spec, input, read)
func AttachRawRead(
	cmd *cobra.Command,
	f *cli.Factory,
	spec audit.Spec,
	input func() (json.RawMessage, error),
	read OperatorRead,
) {
	attachRead(
		cmd,
		f,
		spec,
		input,
		read,
		func(ctx context.Context, payload []byte) error {
			if _, err := cmd.OutOrStdout().Write(payload); err != nil {
				return loggedAuditError(ctx, "write command output", err)
			}

			return nil
		},
	)
}

func attachRead(
	cmd *cobra.Command,
	f *cli.Factory,
	spec audit.Spec,
	input func() (json.RawMessage, error),
	read OperatorRead,
	write func(context.Context, []byte) error,
) {
	if cmd.Annotations == nil {
		cmd.Annotations = make(map[string]string)
	}

	cmd.Annotations[AuditVerbAnnotation] = spec.Verb
	cmd.Annotations[AuditClassAnnotation] = string(spec.Class)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		ctx := cmd.Context()
		encoded, err := input()
		if err != nil {
			return loggedAuditError(ctx, "encode command input", err)
		}

		if !f.Execute() {
			return printReadDryRun(
				withDryRunOutput(ctx, dryRunDestination(f, cmd.OutOrStdout())),
				f,
				spec,
				encoded,
			)
		}

		payload, err := RunAuditedRead(ctx, f, spec, encoded, read)
		if err != nil && !audit.IsReadVerdict(err) {
			return err
		}

		if writeErr := write(ctx, payload); writeErr != nil {
			return writeErr
		}

		return err
	}
}

// printReadDryRun selects the target and resolves the operator, then prints
// the dry-run preamble.
func printReadDryRun(
	ctx context.Context,
	f *cli.Factory,
	spec audit.Spec,
	input json.RawMessage,
) error {
	if _, _, err := operationTarget(ctx, spec, input); err != nil {
		return err
	}

	source := f.OperatorIdentitySource()
	if source == nil {
		return loggedAuditError(
			ctx,
			"resolve operator",
			errors.New("operator identity source is nil"),
		)
	}

	principal, err := source.Resolve(ctx)
	if err != nil {
		return loggedAuditError(ctx, "resolve operator", err)
	}

	return printDryRun(ctx, principal, spec)
}
