package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

type dryRunOutputKey struct{}

func withDryRunOutput(ctx context.Context, output io.Writer) context.Context {
	return context.WithValue(ctx, dryRunOutputKey{}, output)
}

func runAudited(
	ctx context.Context,
	spec audit.Spec,
	source audit.OperatorIdentitySource,
	execute bool,
	outbox audit.OutboxWriter,
	run func(context.Context) error,
) error {
	if spec.Verb == "" {
		return run(ctx)
	}
	if err := validateAuditSpec(spec); err != nil {
		return err
	}
	if source == nil {
		return loggedAuditError(ctx, "resolve operator", errors.New("operator identity source is nil"))
	}
	principal, err := source.Resolve(ctx)
	if err != nil {
		return loggedAuditError(ctx, "resolve operator", err)
	}

	if !execute {
		return printDryRun(ctx, principal, spec)
	}
	opID := uuid.Must(uuid.NewV7())
	event, err := newOperatorEvent(ctx, spec, principal, opID)
	if err != nil {
		return err
	}

	runContext := audit.WithOperatorPrincipal(audit.WithOpID(ctx, opID), principal)
	if outbox == nil {
		return loggedAuditError(ctx, "record audit event", errors.New("audit outbox is nil"))
	}

	if spec.Reads {
		if err := outbox.WriteOutbox(ctx, event); err != nil {
			return loggedAuditError(ctx, "record read access", err)
		}
		return run(runContext)
	}

	intent := event
	intent.Outcome = audit.OutcomePending
	if err := outbox.WriteOutbox(ctx, intent); err != nil {
		return loggedAuditError(ctx, "record command intent", err)
	}

	runErr := run(runContext)

	// The outcome is a second row, so it needs its own identity: the outbox
	// keys on event id and the consumer drops a duplicate, so reusing the
	// intent's id would silently discard the outcome. The shared op id in
	// Extra is what ties the pair together.
	outcome := event
	outcome.EventID = uuid.Must(uuid.NewV7())
	outcome.OccurredAt = clock.Now().UTC()
	if runErr != nil {
		outcome.Outcome = audit.OutcomeError
		outcome.Error = &audit.EventError{Code: "command_failed", Message: runErr.Error()}
	}
	if err := outbox.WriteOutbox(ctx, outcome); err != nil {
		return loggedAuditError(ctx, "record command outcome", err)
	}
	return runErr
}

func validateAuditSpec(spec audit.Spec) error {
	if spec.Atomic {
		// A FoundationDB command records inside its own transaction, and the
		// gate must verify it actually did. That machinery lands with the
		// first such command; until then declaring Atomic fails loudly rather
		// than falling through to the outbox path and quietly losing the
		// guarantee the flag promises.
		return fmt.Errorf("audit spec %q declares Atomic, which is not supported yet", spec.Verb)
	}
	if spec.Mutates == spec.Reads {
		return fmt.Errorf("audit spec %q must mark exactly one command class", spec.Verb)
	}
	return nil
}

// newOperatorEvent builds the event a command records. The op id travels in
// Extra, which the ledger stores and the row hash covers, so the intent row
// and its outcome row can be tied together by a reader and neither can be
// altered without breaking the chain.
func newOperatorEvent(
	ctx context.Context,
	spec audit.Spec,
	principal audit.OperatorPrincipal,
	opID uuid.UUID,
) (audit.Event, error) {
	extra, err := json.Marshal(operatorEventExtra{
		OpID:           opID,
		OperatorSource: principal.Source,
	})
	if err != nil {
		return audit.Event{}, loggedAuditError(ctx, "encode operator event extra", err)
	}
	return audit.Event{
		Verb:    spec.Verb,
		EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type:          audit.ActorOperator,
			ID:            principal.ID,
			Email:         principal.Email,
			Name:          principal.Name,
			SessionID:     "",
			IP:            "",
			UserAgent:     "",
			RequestID:     "",
			APITokenLabel: "",
		},
		Entity: audit.Entity{
			Type:       "system",
			NodeType:   "",
			ID:         audit.SystemOrgID,
			Identifier: "",
			Name:       "",
		},
		Context: audit.EventContext{
			OrgID:       audit.SystemOrgID,
			WorkspaceID: uuid.Nil,
			ScopeID:     uuid.Nil,
			ParentID:    uuid.Nil,
			RequestID:   "",
			TraceID:     "",
			Source:      audit.SourceSystem,
			Tool:        "",
			RPC:         "",
			Reason:      "",
		},
		Delta:          nil,
		Outcome:        audit.OutcomeOK,
		Error:          nil,
		IdempotencyKey: "",
		OccurredAt:     clock.Now().UTC(),
		Extra:          extra,
	}, nil
}

// operatorEventExtra is the correlation payload every operator event carries.
type operatorEventExtra struct {
	// OpID is shared by an intent row and its outcome row.
	OpID uuid.UUID `json:"op_id"`
	// OperatorSource names the mechanism that established the identity, so a
	// reader can tell a git-config identity from an asserted flag.
	OperatorSource string `json:"operator_source"`
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
