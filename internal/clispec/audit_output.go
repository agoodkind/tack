package clispec

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/cli"
)

type dryRunOutputKey struct{}

// dryRunDestination returns the output stream for the dry-run preamble. JSON
// output mode uses stderr for the preamble and stdout for the JSON document.
func dryRunDestination(f *cli.Factory, text io.Writer) io.Writer {
	if f.OutputFormat() == cli.FormatJSON {
		return f.Err
	}
	return text
}

func withDryRunOutput(ctx context.Context, output io.Writer) context.Context {
	return context.WithValue(ctx, dryRunOutputKey{}, output)
}

func printDryRun(
	ctx context.Context,
	principal audit.OperatorPrincipal,
	spec audit.Spec,
) error {
	output, ok := ctx.Value(dryRunOutputKey{}).(io.Writer)
	if !ok || output == nil {
		slog.InfoContext(ctx, "cli.dry_run",
			slog.String("operator_id", principal.ID.String()),
			slog.String("operator_email", principal.Email),
			slog.String("operator_name", principal.Name),
			slog.String("operator_source", principal.Source),
			slog.String("operation", spec.Verb),
		)
		return nil
	}
	_, err := fmt.Fprintf(output,
		"operator: id=%s email=%s name=%s source=%s\nwould run: %s\n",
		principal.ID, principal.Email, principal.Name, principal.Source, spec.Verb)
	if err != nil {
		return loggedAuditError(ctx, "print audit dry-run", err)
	}
	return nil
}

// PrintCommandDryRun resolves the operator with f and prints spec's dry-run
// preamble to f's dry-run output stream. It does not write an audit event.
func PrintCommandDryRun(ctx context.Context, f *cli.Factory, spec audit.Spec) error {
	return RunAudited(ctx, dryRunDestination(f, f.Out), spec, f.OperatorIdentitySource(), f.DeployCommit(), false,
		f.AuditOutbox(), f.AuditInfrastructureProbe(), func(context.Context) error { return nil })
}

func loggedAuditError(ctx context.Context, action string, err error) error {
	slog.ErrorContext(ctx, "cli.audit_failed",
		slog.String("action", action),
		slog.String("err", err.Error()),
	)
	return fmt.Errorf("%s: %w", action, err)
}
