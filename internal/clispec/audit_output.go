package clispec

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/tack/internal/audit"
)

type dryRunOutputKey struct{}

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

func loggedAuditError(ctx context.Context, action string, err error) error {
	slog.ErrorContext(ctx, "cli.audit_failed",
		slog.String("action", action),
		slog.String("err", err.Error()),
	)
	return fmt.Errorf("%s: %w", action, err)
}
