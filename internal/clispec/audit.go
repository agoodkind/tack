package clispec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"goodkind.io/tack/internal/audit"
	"goodkind.io/tack/internal/clock"
)

// RunAudited records an operator command before running it. A command that
// creates the audit infrastructure records after it establishes a missing outbox.
func RunAudited(
	ctx context.Context,
	output io.Writer,
	spec audit.Spec,
	source audit.OperatorIdentitySource,
	deployCommit string,
	execute bool,
	outbox audit.OutboxWriter,
	probe audit.InfrastructureProbe,
	run func(context.Context) error,
) error {
	return runAudited(withDryRunOutput(ctx, output), spec, source, deployCommit, execute, outbox, probe, run)
}

func runAudited(
	ctx context.Context,
	spec audit.Spec,
	source audit.OperatorIdentitySource,
	deployCommit string,
	execute bool,
	outbox audit.OutboxWriter,
	probe audit.InfrastructureProbe,
	run func(context.Context) error,
) error {
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
	event, err := newOperatorEvent(ctx, spec, principal, opID, deployCommit)
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
		if spec.CreatesAuditInfrastructure {
			return runAfterCreatingAuditInfrastructure(
				ctx, intent, event, runContext, outbox, probe, run, err,
			)
		}
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
	if spec.Verb == "" {
		return errors.New("audit spec must declare a verb")
	}
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
	if spec.CreatesAuditInfrastructure && spec.Reads {
		return fmt.Errorf("audit spec %q cannot create audit infrastructure for a read", spec.Verb)
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
	deployCommit string,
) (audit.Event, error) {
	extra, err := json.Marshal(operatorEventExtra{
		OpID:           opID,
		StartedAt:      nil,
		OperatorSource: principal.Source,
		DeployCommit:   strings.TrimSpace(deployCommit),
	})
	if err != nil {
		return audit.Event{}, loggedAuditError(ctx, "encode operator event extra", err)
	}
	return audit.Event{
		Verb:    spec.Verb,
		EventID: uuid.Must(uuid.NewV7()),
		Actor: audit.Actor{
			Type:          principal.ActorType(),
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
			ID:         audit.SystemOrgID(),
			Identifier: "",
			Name:       "",
		},
		Context: audit.EventContext{
			OrgID:       audit.SystemOrgID(),
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
	// StartedAt records when a deferred infrastructure command entered the gate.
	StartedAt *time.Time `json:"started_at,omitempty"`
	// OperatorSource names the mechanism that established the identity, so a
	// reader can tell a git-config identity from an asserted flag.
	OperatorSource string `json:"operator_source"`
	// DeployCommit identifies the opaque commit or branch supplied by the deploy playbook.
	DeployCommit string `json:"deploy_commit,omitempty"`
}
